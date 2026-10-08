// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/Shopify/zk"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/test"
	"go.mongodb.org/mongo-driver/bson"
)

func TestZooKeeperIntegration(t *testing.T) {
	ensemble := test.SetupZooKeeper(t)
	t.Run("transport, persistent nodes and ACLs", func(t *testing.T) { testZooKeeperTransport(t, ensemble.Addresses) })
	t.Run("MongoDB fallback and automatic catch-up", func(t *testing.T) { testZooKeeperFallback(t, ensemble) })
	t.Run("missing session fails immediately", func(t *testing.T) {
		client := newZooKeeperClient(t.Context(), config.SnapshotZooKeeper{
			Addresses: []string{"127.0.0.1:1"}, BasePath: "/test", SessionTimeout: time.Second,
		}, time.Second)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		start := time.Now()
		_, err := client.read(ctx, "prepared")
		require.Error(t, err)
		require.Less(t, time.Since(start), 100*time.Millisecond)
		require.NoError(t, client.close(ctx))
		require.NoError(t, client.close(ctx), "shutdown must be idempotent")
	})
}

func testZooKeeperTransport(t *testing.T, addresses []string) {
	client := testZooKeeperClient(t, addresses)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	missing, err := client.read(ctx, "prepared")
	require.NoError(t, err)
	require.False(t, missing.exists)
	version := testDescriptor(time.Now(), 0)
	prepared, outcome, err := client.write(ctx, "prepared", version, missing)
	require.NoError(t, err)
	require.Equal(t, writeConfirmed, outcome)
	require.True(t, prepared.exists)
	require.Zero(t, prepared.version)
	readBack, err := client.read(ctx, "prepared")
	require.NoError(t, err)
	require.True(t, sameNode(prepared, readBack))
	next := testDescriptor(time.Now(), 1)
	updated, outcome, err := client.write(ctx, "prepared", next, prepared)
	require.NoError(t, err)
	require.Equal(t, writeConfirmed, outcome)
	require.Equal(t, prepared.czxid, updated.czxid)
	require.Equal(t, prepared.version+1, updated.version)
	_, outcome, err = client.write(ctx, "prepared", version, prepared)
	require.ErrorIs(t, err, zk.ErrBadVersion)
	require.Equal(t, writeNotExecuted, outcome)
	_, outcome, err = client.write(ctx, "prepared", version, missing)
	require.ErrorIs(t, err, zk.ErrNodeExists)
	require.Equal(t, writeNotExecuted, outcome)
	conn, _, err := client.connection()
	require.NoError(t, err)
	nodePath := client.config.BasePath + "/prepared"
	for _, node := range []string{"/quasar-test", client.config.BasePath, nodePath} {
		acl, stat, err := conn.GetACL(ctx, node)
		require.NoError(t, err)
		require.Equal(t, zk.WorldACL(zk.PermAll), acl)
		require.Zero(t, stat.EphemeralOwner)
	}
	_, err = conn.Set(ctx, client.config.BasePath, []byte("existing-parent-data"), -1)
	require.NoError(t, err)
	limited := zk.WorldACL(zk.PermRead | zk.PermWrite)
	_, err = conn.SetACL(ctx, nodePath, limited, -1)
	require.NoError(t, err)
	_, outcome, err = client.write(ctx, "prepared", version, updated)
	require.NoError(t, err)
	require.Equal(t, writeConfirmed, outcome)
	acl, _, err := conn.GetACL(ctx, nodePath)
	require.NoError(t, err)
	require.Equal(t, limited, acl, "data updates must not replace ACLs")
	parentData, _, err := conn.Get(ctx, client.config.BasePath)
	require.NoError(t, err)
	require.Equal(t, []byte("existing-parent-data"), parentData)
	_, err = conn.Create(ctx, client.config.BasePath+"/activated", []byte("{}"), 0, zk.WorldACL(zk.PermAll))
	require.NoError(t, err)
	_, err = client.read(ctx, "activated")
	require.Error(t, err, "invalid data is not an absent bootstrap node")
	_, err = conn.Create(ctx, client.config.BasePath+"/ephemeral", []byte("{}"), zk.FlagEphemeral, zk.WorldACL(zk.PermAll))
	require.NoError(t, err)
	_, err = client.read(ctx, "ephemeral")
	require.Error(t, err)
	staleSession := updated
	staleSession.session++
	_, outcome, err = client.write(ctx, "prepared", next, staleSession)
	require.Error(t, err)
	require.Equal(t, writeNotExecuted, outcome)
	testZooKeeperExistingParentACL(t, client)
}

func testZooKeeperExistingParentACL(t *testing.T, client *zooKeeperClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, _, err := client.connection()
	require.NoError(t, err)
	parent := client.config.BasePath + "/read-only-ancestor"
	base := parent + "/existing"
	_, err = conn.Create(ctx, parent, nil, 0, zk.WorldACL(zk.PermAll))
	require.NoError(t, err)
	_, err = conn.Create(ctx, base, []byte("preserved"), 0, zk.WorldACL(zk.PermAll))
	require.NoError(t, err)
	readOnly := zk.WorldACL(zk.PermRead)
	_, err = conn.SetACL(ctx, parent, readOnly, -1)
	require.NoError(t, err)
	cfg := client.config
	cfg.BasePath = base
	nested := newZooKeeperClient(t.Context(), cfg, time.Second)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		require.NoError(t, nested.close(ctx))
	})
	require.Eventually(t, nested.available, 10*time.Second, 20*time.Millisecond)
	missing, err := nested.read(ctx, preparedNode)
	require.NoError(t, err)
	_, outcome, err := nested.write(ctx, preparedNode, testDescriptor(time.Now(), 0), missing)
	require.NoError(t, err, "existing ancestors must not need create permissions")
	require.Equal(t, writeConfirmed, outcome)
	acl, _, err := conn.GetACL(ctx, parent)
	require.NoError(t, err)
	require.Equal(t, readOnly, acl)
	data, _, err := conn.Get(ctx, base)
	require.NoError(t, err)
	require.Equal(t, []byte("preserved"), data)
}

func testZooKeeperFallback(t *testing.T, ensemble *test.ZooKeeperEnsemble) {
	uri := test.SetupMongoReplicaSet(t)
	_, store, w := mongoFixture(t, uri)
	client := testZooKeeperClient(t, ensemble.Addresses)
	w.config.ActivationDelay = 100 * time.Millisecond
	w.publication = newZooKeeperPublication(client, w.config.ActivationDelay)
	advance := func() {
		t.Helper()
		w.publication.retryBlocked = false
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		err := w.progressZooKeeper(ctx)
		if err != nil {
			w.publication.degrade(err)
		}
		if w.proposal != nil && w.publication.mongoAllowed(w.proposedSnapshot()) {
			require.NoError(t, w.resolve(ctx))
		}
	}
	require.NoError(t, w.refreshSnapshot(t.Context()))
	advance()
	time.Sleep(w.config.ActivationDelay)
	advance()
	advance()
	require.Nil(t, w.proposal)
	require.Nil(t, w.publication.candidate)
	a := w.publication.observed.activated.value
	ensemble.Pause(t, 0)
	_, err := store.source.ReplaceOne(t.Context(), bson.D{{Key: "_id", Value: "a"}}, sourceDocument(t, "a", "one member down"))
	require.NoError(t, err)
	require.NoError(t, w.refreshSnapshot(t.Context()))
	advance()
	time.Sleep(w.config.ActivationDelay)
	advance()
	advance()
	require.Nil(t, w.proposal, "one failed member must not prevent normal publication")
	ensemble.Pause(t, 1)
	ensemble.Pause(t, 2)
	require.Eventually(t, func() bool { return !client.available() }, 15*time.Second, 20*time.Millisecond)
	for i := range 5 {
		_, err := store.source.ReplaceOne(t.Context(), bson.D{{Key: "_id", Value: "a"}},
			sourceDocument(t, "a", string(rune('b'+i))))
		require.NoError(t, err)
		require.NoError(t, w.refreshSnapshot(t.Context()))
		advance()
		require.Nil(t, w.proposal, "MongoDB heads must continue while quorum is lost")
		require.Error(t, w.cleanupPending(t.Context()))
	}
	current, err := store.readHead(t.Context())
	require.NoError(t, err)
	latest := current.Version
	for i := range 3 {
		ensemble.Resume(t, i)
	}
	require.Eventually(t, client.available, 30*time.Second, 20*time.Millisecond)
	advance()
	time.Sleep(w.config.ActivationDelay)
	advance()
	advance()
	require.Nil(t, w.publication.candidate)
	require.True(t, sameDescriptor(latest, w.publication.observed.activated.value))
	after, err := store.readHead(t.Context())
	require.NoError(t, err)
	require.True(t, sameHead(current, after), "reconnect must not modify the MongoDB head or history")
	require.NotEqual(t, a.SnapshotID, latest.SnapshotID)
	require.NoError(t, w.cleanupPending(t.Context()))
}

func TestZooKeeperSDKLogsDiscardNonErrorAttributes(t *testing.T) {
	var output bytes.Buffer
	handler := &zooKeeperLogHandler{logger: zerolog.New(&output)}
	logger := slog.New(handler).With("password", "never-log-secret").WithGroup("wire")
	logger.ErrorContext(t.Context(), "recv loop terminated", "payload", `{"subscription":"private"}`,
		"error", errors.New("socket read failed"))
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	require.Equal(t, "recv loop terminated", record["message"])
	require.Equal(t, "error", record["level"])
	require.Equal(t, "zookeeper-sdk", record["source"])
	require.Equal(t, "socket read failed", record["error"])
	require.NotContains(t, record, "errorCategory")
	require.Len(t, record, 4, "only message, level, source and the original error are forwarded")
	for _, sensitive := range []string{"never-log-secret", "private", "password", "payload", "wire"} {
		require.NotContains(t, output.String(), sensitive)
	}
}

func TestZooKeeperSDKLogLevels(t *testing.T) {
	for _, tt := range []struct {
		name  string
		level slog.Level
		want  zerolog.Level
	}{
		{"debug", slog.LevelDebug, zerolog.DebugLevel},
		{"info", slog.LevelInfo, zerolog.InfoLevel},
		{"warn", slog.LevelWarn, zerolog.WarnLevel},
		{"error", slog.LevelError, zerolog.ErrorLevel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, zooKeeperLogLevel(tt.level))
			var output bytes.Buffer
			handler := &zooKeeperLogHandler{logger: zerolog.New(&output).Level(zerolog.DebugLevel)}
			logger := slog.New(handler)
			logger.Log(t.Context(), tt.level, "connected", "password", "never-log-secret")
			entries := serviceLogMessages(t, output.Bytes())
			require.Equal(t, []string{"connected"}, messageNames(entries))
			require.Equal(t, tt.name, entries[0]["level"])
			require.Equal(t, "zookeeper-sdk", entries[0]["source"])
			require.NotContains(t, entries[0], "errorCategory")
			require.NotContains(t, entries[0], "error")
			require.NotContains(t, output.String(), "never-log-secret")
			filtered := &zooKeeperLogHandler{logger: zerolog.New(&output).Level(zerolog.WarnLevel)}
			require.Equal(t, tt.level >= slog.LevelWarn, filtered.Enabled(t.Context(), tt.level))
		})
	}
}

func TestZooKeeperSDKLogMessages(t *testing.T) {
	for _, tt := range []struct {
		message string
		err     error
	}{
		{"connected", nil},
		{"authenticated", nil},
		{"re-submitting credentials after reconnect", nil},
		{"send loop terminated", nil},
		{"a new SDK message", nil},
		{"recv loop terminated", io.EOF},
	} {
		t.Run(tt.message, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(&zooKeeperLogHandler{logger: zerolog.New(&output)})
			logger.InfoContext(t.Context(), tt.message, "error", tt.err,
				"server", "private-server:2181", "id", 123, "timeout", 10000, "count", 0)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, tt.message, entry["message"])
			require.Equal(t, "info", entry["level"])
			require.Equal(t, "zookeeper-sdk", entry["source"])
			require.NotContains(t, entry, "errorCategory")
			if tt.err == nil {
				require.NotContains(t, entry, "error")
				require.Len(t, entry, 3)
			} else {
				require.Equal(t, tt.err.Error(), entry["error"])
				require.Len(t, entry, 4)
			}
			require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("\n")), "one record produces one immediate log")
			require.NotContains(t, output.String(), "private-server")
		})
	}
}

func TestZooKeeperSDKLogErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"EOF", io.EOF},
		{"wrapped EOF", &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}},
		{"network timeout", &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}},
		{"network address", &net.OpError{
			Op:   "read",
			Net:  "tcp",
			Addr: &net.TCPAddr{IP: net.IPv4(192, 0, 2, 20), Port: 2181},
			Err:  io.EOF,
		}},
		{"cancelled", context.Canceled},
		{"deadline exceeded", context.DeadlineExceeded},
		{"missing", zk.ErrNoNode},
		{"exists", zk.ErrNodeExists},
		{"version conflict", zk.ErrBadVersion},
		{"access", zk.ErrNoAuth},
		{"unavailable", zk.ErrConnectionClosed},
		{"unknown", errors.New("unexpected transport failure")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(&zooKeeperLogHandler{logger: zerolog.New(&output)})
			logger.InfoContext(t.Context(), "recv loop terminated", "error", tt.err)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, "recv loop terminated", entry["message"])
			require.Equal(t, "info", entry["level"])
			require.Equal(t, "zookeeper-sdk", entry["source"])
			require.Equal(t, tt.err.Error(), entry["error"])
			var networkError *net.OpError
			if errors.As(tt.err, &networkError) && networkError.Addr != nil {
				require.Contains(t, entry["error"], networkError.Addr.String())
			}
			require.NotContains(t, entry, "errorCategory")
			require.Len(t, entry, 4)
		})
	}
}

type zooKeeperSDKLogValuer struct {
	value slog.Value
}

func (v zooKeeperSDKLogValuer) LogValue() slog.Value {
	return v.value
}

func TestZooKeeperSDKLogAttributeHandling(t *testing.T) {
	for _, tt := range []struct {
		name      string
		log       func(*slog.Logger)
		errorText string
	}{
		{"no attributes", func(logger *slog.Logger) {
			logger.InfoContext(t.Context(), "connected")
		}, ""},
		{"inherited error", func(logger *slog.Logger) {
			logger.With("error", io.EOF, "password", "private-password").InfoContext(t.Context(), "recv loop terminated")
		}, "EOF"},
		{"nested error", func(logger *slog.Logger) {
			logger.LogAttrs(t.Context(), slog.LevelInfo, "recv loop terminated",
				slog.Group("wire", slog.Group("nested", slog.Any("error", io.EOF), slog.String("password", "private-password"))))
		}, "EOF"},
		{"last error in group wins", func(logger *slog.Logger) {
			logger.LogAttrs(t.Context(), slog.LevelInfo, "recv loop terminated",
				slog.Group("wire", slog.Any("error", io.EOF), slog.Any("error", context.Canceled)))
		}, context.Canceled.Error()},
		{"log valuer error", func(logger *slog.Logger) {
			logger.InfoContext(t.Context(), "recv loop terminated",
				"error", zooKeeperSDKLogValuer{value: slog.AnyValue(io.EOF)})
		}, "EOF"},
		{"log valuer group", func(logger *slog.Logger) {
			logger.InfoContext(t.Context(), "recv loop terminated", "wire", zooKeeperSDKLogValuer{
				value: slog.GroupValue(slog.Any("error", io.EOF), slog.String("password", "private-password")),
			})
		}, "EOF"},
		{"group without error", func(logger *slog.Logger) {
			logger.LogAttrs(t.Context(), slog.LevelInfo, "connected",
				slog.Group("wire", slog.String("password", "private-password")))
		}, ""},
		{"string error", func(logger *slog.Logger) {
			logger.InfoContext(t.Context(), "recv loop terminated", "error", "private-error-detail")
		}, ""},
		{"error object under another key", func(logger *slog.Logger) {
			logger.InfoContext(t.Context(), "connected", "payload", errors.New("private-error-detail"))
		}, ""},
		{"record error overrides inherited error", func(logger *slog.Logger) {
			logger.With("error", io.EOF).InfoContext(t.Context(), "recv loop terminated", "error", context.Canceled)
		}, context.Canceled.Error()},
		{"record nil overrides inherited error", func(logger *slog.Logger) {
			logger.With("error", io.EOF).InfoContext(t.Context(), "send loop terminated", "error", nil)
		}, ""},
		{"record error overrides nested inherited error", func(logger *slog.Logger) {
			logger.With(slog.Group("wire", slog.Any("error", io.EOF))).
				InfoContext(t.Context(), "recv loop terminated", "error", context.Canceled)
		}, context.Canceled.Error()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			handler := &zooKeeperLogHandler{logger: zerolog.New(&output)}
			tt.log(slog.New(handler))
			entries := serviceLogMessages(t, output.Bytes())
			require.Len(t, entries, 1)
			require.NotContains(t, entries[0], "errorCategory")
			if tt.errorText == "" {
				require.NotContains(t, entries[0], "error")
				require.Len(t, entries[0], 3)
			} else {
				require.Equal(t, tt.errorText, entries[0]["error"])
				require.Len(t, entries[0], 4)
			}
			for _, sensitive := range []string{"private-password", "private-error-detail", "wire", "nested", "password"} {
				require.NotContains(t, output.String(), sensitive)
			}
			require.Empty(t, handler.attrs, "WithAttrs must not mutate the original handler")
		})
	}
}
