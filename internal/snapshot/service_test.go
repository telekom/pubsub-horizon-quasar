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
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/mongo"
)

// TestSchedulingRefreshAndCleanupAfterPublication checks periodic scans and cleanup only after confirmed publication.
func TestSchedulingRefreshAndCleanupAfterPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Minute)
		require.Equal(t, 1, store.sourceReads, "startup runs without waiting for a tick")
		require.Equal(t, 1, store.activations)
		require.Equal(t, 1, store.cleanups, "the initial snapshot is cleaned immediately")

		orphan := testDescriptor(time.Now().Add(-24*time.Hour), 1)
		store.versions[orphan.SnapshotID] = 1
		s.advance(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		require.Equal(t, 1, store.activations, "unchanged checks must not publish")
		require.Equal(t, 1, store.cleanups, "cleanup must not run on a timer")
		require.Contains(t, store.versions, orphan.SnapshotID)

		store.source[0] = sourceDocument(t, "a", "changed")
		s.advance(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.activations)
		require.Equal(t, 2, store.cleanups, "publication immediately triggers cleanup")
		require.NotContains(t, store.versions, orphan.SnapshotID)

		reads := store.sourceReads
		s.advance(10 * time.Minute)
		synctest.Wait()
		require.Equal(t, reads+10, store.sourceReads)
		require.Equal(t, 2, store.cleanups, "unchanged refreshes must not trigger cleanup")
		s.shutdown()
	})
}

// TestSchedulingCoalescesTicksWhileRefreshIsRunning checks that blocked scans do not overlap and missed ticks coalesce.
func TestSchedulingCoalescesTicksWhileRefreshIsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Minute)
		release := make(chan struct{})
		store.beforeRead = func(ctx context.Context) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		s.advance(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		s.advance(5 * time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads, "ticks must not launch concurrent scans")
		require.Equal(t, 1, store.cleanups, "cleanup must not run during a blocked, unchanged refresh")

		store.source[0] = sourceDocument(t, "a", "changed while blocked")
		close(release)
		synctest.Wait()
		require.Equal(t, 3, store.sourceReads, "missed ticks coalesce to one pending refresh")
		require.Equal(t, 2, store.activations)
		require.Equal(t, 2, store.cleanups)
		s.shutdown()
	})
}

// TestSchedulingCancelsInflightRefresh checks prompt shutdown and preservation of the head during a blocked scan.
func TestSchedulingCancelsInflightRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Minute)
		var readErr error
		store.beforeRead = func(ctx context.Context) error {
			<-ctx.Done()
			readErr = ctx.Err()
			return readErr
		}
		s.advance(time.Minute)
		synctest.Wait()
		before := store.current
		start := time.Now()
		s.shutdown()
		synctest.Wait()
		require.ErrorIs(t, readErr, context.Canceled)
		require.Less(t, time.Since(start), shutdownTimeout)
		require.True(t, sameHead(before, store.current))
		require.Equal(t, 1, store.activations)
	})
}

// TestSchedulingUnresolvedActivationBlocksCleanup checks that uncertain head updates retain their proposal until recovery.
func TestSchedulingUnresolvedActivationBlocksCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		store.activation = func(string, head) (bool, error) {
			return false, context.DeadlineExceeded
		}
		s := runScheduledService(t, store, time.Minute)
		require.NotNil(t, s.worker.proposal)
		require.Zero(t, store.cleanups)
		require.Equal(t, 1, store.inserts)

		store.activation = nil
		s.advance(time.Minute)
		synctest.Wait()
		require.Nil(t, s.worker.proposal)
		require.Equal(t, 1, store.inserts, "recovery must reuse the exact buffered candidate")
		require.Equal(t, 1, store.cleanups, "cleanup follows confirmed activation")
		s.shutdown()
	})
}

// TestCleanupFailureIsLoggedAndRetriedOnNextRefresh checks cleanup error logging and retry without a new publication.
func TestCleanupFailureIsLoggedAndRetriedOnNextRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Minute)
		orphan := testDescriptor(time.Now(), 1)
		store.versions[orphan.SnapshotID] = 1
		s.worker.cleanupDue = true
		s.worker.store = &faultStore{
			snapshotStore: store,
			delete: func(context.Context, string) (int64, error) {
				return 0, context.DeadlineExceeded
			},
		}

		var output bytes.Buffer
		previousLogger := log.Logger
		log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
		t.Cleanup(func() { log.Logger = previousLogger })
		s.attemptRefresh()
		lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
		require.Len(t, lines, 1)
		var entry map[string]any
		require.NoError(t, json.Unmarshal(lines[0], &entry))
		require.Equal(t, "Subscription snapshot operation failed", entry["message"])
		require.Equal(t, "error", entry["level"])
		require.Equal(t, "cleanup", entry["operation"])
		require.True(t, s.worker.cleanupDue)

		output.Reset()
		s.worker.store = store
		s.attemptRefresh()
		require.Empty(t, output.String(), "successful cleanup and unchanged scans are silent at INFO")
		require.False(t, s.worker.cleanupDue)
		require.NotContains(t, store.versions, orphan.SnapshotID)
		s.shutdown()
	})
}

// TestCleanupUsesIndependentTimeout checks that cleanup can finish beyond the refresh timeout.
func TestCleanupUsesIndependentTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Hour)
		s.config.RefreshTimeout = time.Minute
		s.config.CleanupTimeout = 3 * time.Minute
		store.beforeHead = func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Equal(t, 3*time.Minute, time.Until(deadline))
			time.Sleep(2 * time.Minute)
			return nil
		}
		s.worker.cleanupDue = true

		s.attemptCleanup()

		require.False(t, s.worker.cleanupDue, "cleanup should complete beyond refresh timeout")
		require.Equal(t, 2, store.cleanups)
		s.shutdown()
	})
}

// TestPendingCleanupRunsAfterRefreshTimeout checks that a failed scan does not consume pending cleanup's time budget.
func TestPendingCleanupRunsAfterRefreshTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Hour)
		s.config.RefreshTimeout = time.Minute
		s.config.CleanupTimeout = 3 * time.Minute
		s.worker.cleanupDue = true
		orphan := testDescriptor(time.Now(), 1)
		store.versions[orphan.SnapshotID] = 1
		store.beforeRead = func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}

		s.attemptRefresh()

		require.False(t, s.worker.cleanupDue, "independent cleanup should run after refresh context expires")
		require.NotContains(t, store.versions, orphan.SnapshotID)
		require.Equal(t, 2, store.cleanups)
		s.shutdown()
	})
}

// TestPublicationLoggingDurationMs checks separate creation, publication and cleanup logs with millisecond durations.
func TestPublicationLoggingDurationMs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		store.beforeRead = func(context.Context) error {
			time.Sleep(time.Second)
			return nil
		}
		var output bytes.Buffer
		previousLogger := log.Logger
		log.Logger = zerolog.New(&output).Level(zerolog.DebugLevel)
		t.Cleanup(func() { log.Logger = previousLogger })

		require.NoError(t, newWorker(testConfig(), store).refresh(t.Context()))

		lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
		require.Len(t, lines, 3, "creation, publication and immediate cleanup each emit one log event")
		var created map[string]any
		require.NoError(t, json.Unmarshal(lines[0], &created))
		require.Equal(t, "Subscription snapshot created", created["message"])
		require.Equal(t, "info", created["level"])
		require.Equal(t, float64(1000), created["durationMs"])
		var publication map[string]any
		require.NoError(t, json.Unmarshal(lines[1], &publication))
		require.Equal(t, "Subscription snapshot published", publication["message"])
		require.Equal(t, float64(1000), publication["durationMs"])
		require.NotContains(t, publication, "duration")
		require.Equal(t, store.current.Version.SnapshotID, publication["snapshotId"])
		require.Equal(t, "initial", publication["snapshotReason"])
		var cleanup map[string]any
		require.NoError(t, json.Unmarshal(lines[2], &cleanup))
		require.Equal(t, "Subscription snapshot cleanup completed", cleanup["message"])
		require.Equal(t, "debug", cleanup["level"])
		require.Equal(t, float64(0), cleanup["deletedDocuments"])
	})
}

// TestSnapshotServiceFailureLogging checks failure stages and confirms that ZooKeeper errors still allow MongoDB fallback.
func TestSnapshotServiceFailureLogging(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*fakeStore, *fakeZooKeeper)
		operation string
		created   int
	}{
		{"scan", func(store *fakeStore, _ *fakeZooKeeper) { store.sourceError = context.DeadlineExceeded }, "refresh", 0},
		{"insert", func(store *fakeStore, _ *fakeZooKeeper) { store.insertError = context.DeadlineExceeded }, "refresh", 0},
		{"MongoDB publication", func(store *fakeStore, _ *fakeZooKeeper) {
			store.activation = func(string, head) (bool, error) { return false, context.DeadlineExceeded }
		}, "activate-mongo", 1},
		{"ZooKeeper preparation", func(_ *fakeStore, z *fakeZooKeeper) {
			z.beforeWrite = func(context.Context, string, descriptor, zNode) (writeOutcome, error) {
				return writeUncertain, context.DeadlineExceeded
			}
		}, "zookeeper", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store, z := newFakeStore(t), newFakeZooKeeper()
				tt.mutate(store, z)
				c := testConfig()
				c.ActivationDelay = 0
				s := newService(c)
				defer s.cancel()
				s.client, s.store, s.session = &mongo.Client{}, &mongoStore{}, &fakeSession{}
				s.worker = newWorker(c, store)
				s.zooKeeper = z
				var output bytes.Buffer
				previous := log.Logger
				log.Logger = zerolog.New(&output)
				t.Cleanup(func() { log.Logger = previous })

				s.attemptRefresh()

				require.Equal(t, tt.created, bytes.Count(output.Bytes(), []byte("Subscription snapshot created")))
				require.NotContains(t, output.String(), "Subscription snapshot source unchanged")
				require.NotContains(t, output.String(), "Subscription snapshot ZooKeeper activated")
				errorsLogged := 0
				for _, entry := range serviceLogMessages(t, output.Bytes()) {
					if entry["level"] == "error" && entry["operation"] == tt.operation {
						errorsLogged++
						require.IsType(t, float64(0), entry["durationMs"])
					}
				}
				require.Equal(t, 1, errorsLogged)
				switch tt.operation {
				case "refresh":
					require.Nil(t, s.worker.proposal)
					require.Zero(t, store.activations)
				case "activate-mongo":
					require.NotNil(t, s.worker.proposal)
					require.Empty(t, store.current.Version.SnapshotID)
					require.NotContains(t, output.String(), "Subscription snapshot published")
				default:
					require.Nil(t, s.worker.proposal)
					require.NotEmpty(t, store.current.Version.SnapshotID, "ZooKeeper failures preserve MongoDB fallback")
					require.Contains(t, output.String(), "Subscription snapshot published")
				}
			})
		})
	}
}

const snapshotTestURIEnv = "QUASAR_TEST_SNAPSHOT_URI"

// TestSnapshotConnectionProcess is a subprocess entry point for initial connection failures, skipped in normal runs.
func TestSnapshotConnectionProcess(t *testing.T) {
	uri := os.Getenv(snapshotTestURIEnv)
	if uri == "" {
		t.Skip("only executed as a subprocess")
	}
	if strings.HasPrefix(uri, "mongodb+srv://") {
		net.DefaultResolver = &net.Resolver{
			PreferGo: true,
			Dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("probe-password DNS failure")
			},
		}
	}
	log.Logger = zerolog.New(os.Stdout)
	c := testConfig()
	c.URI = uri
	c.RefreshTimeout = 2 * time.Second
	c.CleanupTimeout = 2 * time.Second
	require.NoError(t, c.Validate())
	s := newService(c)
	defer s.cancel()
	defer s.disconnect()
	ctx, cancel := context.WithTimeout(t.Context(), c.RefreshTimeout)
	defer cancel()
	require.NoError(t, s.initialize(ctx))
}

// requireSnapshotConnectionFatal checks that a child exits with a fatal connection log without exposing sensitive values.
func requireSnapshotConnectionFatal(t *testing.T, uri, operation string, sensitive ...string) string {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestSnapshotConnectionProcess$")
	command.Env = append(os.Environ(), snapshotTestURIEnv+"="+uri)
	output, err := command.CombinedOutput()
	require.NoError(t, ctx.Err(), "the child must exit itself, not be killed by the test timeout: %s", output)
	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError, string(output))
	require.Equal(t, 1, exitError.ExitCode(), string(output))
	text := string(output)
	require.Contains(t, text, `"level":"fatal"`)
	require.Contains(t, text, `"message":"Subscription snapshot connection failed"`)
	require.Contains(t, text, operation+" dedicated subscription snapshot client failed")
	for _, value := range append([]string{uri, "mongodb://", "mongodb+srv://"}, sensitive...) {
		require.NotContains(t, text, value)
	}
	return text
}

// TestInitialSnapshotConnectionFailures checks fatal, sanitized handling of URI, discovery and initial ping failures.
func TestInitialSnapshotConnectionFailures(t *testing.T) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	tests := []struct {
		name      string
		uri       string
		operation string
	}{
		{"scheme", "https://probe-user:probe-password@example.invalid", "connect"},
		{"option", "mongodb://probe-user:probe-password@localhost/?connectTimeoutMS=probe-password", "connect"},
		{"credential escape", "mongodb://probe-user:probe%zz-password@localhost", "connect"},
		{"SRV port", "mongodb+srv://probe-user:probe-password@example.invalid:27017", "connect"},
		{
			"SRV discovery",
			"mongodb+srv://probe-user:probe-password@example.invalid/?srvServiceName=custom&authSource=private-auth",
			"connect",
		},
		{"ping timeout", "mongodb://probe-user:probe-password@" + listener.Addr().String(), "ping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireSnapshotConnectionFatal(t, tt.uri, tt.operation, "probe-user", "probe-password", "probe%zz-password", "private-auth")
		})
	}
}

// TestDisabledServiceHasNoSideEffects checks disabled startup and rejection of invalid enabled settings before logging.
func TestDisabledServiceHasNoSideEffects(t *testing.T) {
	var output bytes.Buffer
	previousLogger := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = previousLogger })

	require.NoError(t, Start(config.SubscriptionSnapshots{}))
	require.JSONEq(t, `{"level":"info","enabled":false,"message":"Subscription snapshots disabled"}`, output.String())
	output.Reset()
	disabled := testConfig()
	disabled.Enabled = false
	disabled.URI = "mongodb+srv://example.invalid:27017/?journal=false"
	require.NoError(t, Start(disabled), "disabled snapshots must not parse or connect to MongoDB")
	require.JSONEq(t, `{"level":"info","enabled":false,"message":"Subscription snapshots disabled"}`, output.String())
	output.Reset()
	invalid := testConfig()
	invalid.URI = ""
	require.Error(t, Start(invalid))
	require.Empty(t, output.String(), "invalid configuration must not announce startup")
}

// TestStartupLogging checks one startup event with exact settings and no MongoDB credentials.
func TestStartupLogging(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			previousLogger := log.Logger
			log.Logger = zerolog.New(&output)
			t.Cleanup(func() { log.Logger = previousLogger })

			c := testConfig()
			c.Enabled = enabled
			c.URI = "mongodb://snapshot-user:test-password@localhost:27017/?authSource=private-auth"
			c.RefreshInterval = 37 * time.Second
			c.InitialRefreshDelay = 2 * time.Minute
			c.MinimumRetainedSnapshots = 4
			c.MaxSnapshotBytes = 33554432
			c.RefreshTimeout = 7 * time.Second
			logStartup(c)

			expected := `{"level":"info","enabled":false,"message":"Subscription snapshots disabled"}`
			if enabled {
				expected = `{
					"level":"info",
					"enabled":true,
					"database":"test-horizon-config",
					"sourceCollection":"subscriptions",
					"snapshotCollection":"snapshots",
					"headCollection":"heads",
					"refreshInterval":"37s",
					"initialRefreshDelay":"2m0s",
					"minimumRetainedSnapshots":4,
					"maxSnapshotBytes":33554432,
					"refreshTimeout":"7s",
					"cleanupTimeout":"2m0s",
					"activationDelay":"1m0s",
					"zookeeperBasePath":"/horizon/subscriptions",
					"zookeeperClient":"Shopify/zk",
					"message":"Starting subscription snapshot worker"
				}`
			}
			require.JSONEq(t, expected, output.String())
			require.Equal(t, 1, strings.Count(output.String(), "\n"), "startup must emit exactly one event")
			for _, sensitive := range []string{c.URI, "snapshot-user", "test-password", "private-auth"} {
				require.NotContains(t, output.String(), sensitive)
			}
		})
	}
}

// TestServiceSchedulingAndCancellation checks cancellation without timer events and repeated, safe shutdown calls.
func TestServiceSchedulingAndCancellation(t *testing.T) {
	s := newService(testConfig())
	// A canceled context must stop a service even when no ticker ever fires.
	s.cancel()
	refresh := make(chan time.Time)
	go func() {
		defer close(s.done)
		defer s.disconnect()
		s.loop(refresh, nil)
	}()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop on cancellation")
	}
	s.shutdown()
	s.shutdown()
}

// TestCleanupPendingPublicationIsDeferred checks warnings and retained cleanup work before publication completes.
func TestCleanupPendingPublicationIsDeferred(t *testing.T) {
	for _, starting := range []bool{true, false} {
		name := "follow-up proposal"
		if starting {
			name = "startup"
		}
		t.Run(name, func(t *testing.T) {
			store := newFakeStore(t)
			w := newWorker(testConfig(), store)
			if !starting {
				require.NoError(t, w.refresh(t.Context()))
				store.source[0] = sourceDocument(t, "a", "next")
				require.NoError(t, w.createSnapshot(t.Context()))
			}
			w.cleanupDue = true
			pending := w.proposal
			cleanups := store.cleanups
			require.ErrorIs(t, w.cleanupPending(t.Context()), errCleanupDeferred)

			s := newService(w.config)
			defer s.cancel()
			s.client, s.session, s.worker = &mongo.Client{}, &fakeSession{}, w
			var output bytes.Buffer
			previous := log.Logger
			log.Logger = zerolog.New(&output)
			t.Cleanup(func() { log.Logger = previous })
			s.attemptCleanup()

			entries := serviceLogMessages(t, output.Bytes())
			require.Len(t, entries, 1)
			require.Equal(t, "warn", entries[0]["level"])
			require.Equal(t, "cleanup", entries[0]["operation"])
			require.Equal(t, "Subscription snapshot cleanup deferred", entries[0]["message"])
			require.True(t, w.cleanupDue)
			require.Same(t, pending, w.proposal)
			require.Equal(t, cleanups, store.cleanups)
			require.Empty(t, store.deletions)
		})
	}
}

// TestQueuedRefreshContinuesPublicationInSameCycle checks one follow-up scan and publication without waiting for another event.
func TestQueuedRefreshContinuesPublicationInSameCycle(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "healthy", true: "fallback"}[fallback], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store, z := newFakeStore(t), newFakeZooKeeper()
				c := testConfig()
				c.RefreshInterval = 20 * time.Second
				c.ActivationDelay = 75 * time.Second
				s := configuredPublicationService(t, c, store, z)
				a := z.nodes[preparedNode].value
				preparedAt := s.worker.publication.candidate.preparedAt
				store.source[0] = sourceDocument(t, "a", "successor B")
				var output bytes.Buffer
				previous := log.Logger
				log.Logger = zerolog.New(&output)
				t.Cleanup(func() { log.Logger = previous })

				s.advance(time.Minute)
				synctest.Wait()
				require.True(t, s.refreshRequested)
				require.Equal(t, 1, store.sourceReads)
				require.Equal(t, 1, store.inserts)
				require.Zero(t, bytes.Count(output.Bytes(), []byte("Subscription snapshot refresh completed")))
				require.Empty(t, serviceLogMessages(t, output.Bytes()), "queued ticks perform no snapshot work")
				output.Reset()
				if fallback {
					signalZooKeeper(z, false)
					synctest.Wait()
				} else {
					s.advance(15 * time.Second)
					synctest.Wait()
				}

				require.False(t, s.refreshRequested)
				require.Equal(t, 2, store.sourceReads, "all queued ticks coalesce into one inline scan")
				require.Equal(t, 2, store.inserts)
				require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("Subscription snapshot created")))
				serviceLogMessages(t, output.Bytes())
				require.Contains(t, output.String(), `"level":"warn"`)
				require.NotContains(t, output.String(), `"level":"error"`)
				require.Contains(t, output.String(), `"message":"Subscription snapshot cleanup deferred"`)
				require.Equal(t, 1, regularCleanupAttempts(output.Bytes()), "include blocked/deferred attempts, not just deletions")
				require.True(t, s.worker.cleanupDue)
				if fallback {
					require.Equal(t, 2, store.activations, "B receives its own CAS without another event")
					require.Nil(t, s.worker.proposal)
					require.NotEqual(t, a.SnapshotID, store.current.Version.SnapshotID)
					require.True(t, sameDescriptor(a, s.worker.publication.candidate.value))
					require.Equal(t, preparedAt, s.worker.publication.candidate.preparedAt)
					require.Equal(t, []string{preparedNode}, z.writes)
				} else {
					b := s.worker.proposal.next.Version
					require.True(t, sameDescriptor(b, z.nodes[preparedNode].value), "B is prepared before another tick/wake")
					require.True(t, sameDescriptor(a, store.current.Version))
					require.Equal(t, 1, store.activations)
					require.True(t, sameDescriptor(a, z.nodes[activatedNode].value))
					require.Equal(t, []string{preparedNode, activatedNode, preparedNode}, z.writes)
					require.Equal(t, time.Now(), s.worker.publication.candidate.preparedAt)
				}
				s.advance(time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 2, store.sourceReads, "no follow-up polling loop")
				if !fallback {
					b := s.worker.proposal.next.Version
					s.advance(c.ActivationDelay - 2*time.Nanosecond)
					synctest.Wait()
					require.True(t, sameDescriptor(a, store.current.Version), "B cannot publish before its own deadline")
					require.True(t, sameDescriptor(a, z.nodes[activatedNode].value))
					require.Equal(t, 1, store.activations)
					s.advance(time.Nanosecond)
					synctest.Wait()
					require.True(t, sameDescriptor(b, store.current.Version))
					require.True(t, sameDescriptor(b, z.nodes[activatedNode].value))
					require.Equal(t, 2, store.activations)
				}
				s.shutdown()
			})
		})
	}
}

// TestQueuedRefreshDoesNotRepeatFailedOrUnchangedScan checks that a queued scan runs once even without a new snapshot.
func TestQueuedRefreshDoesNotRepeatFailedOrUnchangedScan(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "failed"}[failed], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, _ := publicationService(t, time.Hour)
				if failed {
					store.sourceError = context.DeadlineExceeded
				}
				var output bytes.Buffer
				previous := log.Logger
				log.Logger = zerolog.New(&output)
				t.Cleanup(func() { log.Logger = previous })
				s.attemptRefresh()
				require.True(t, s.refreshRequested)
				require.Equal(t, 1, store.sourceReads)
				require.Zero(t, bytes.Count(output.Bytes(), []byte("Subscription snapshot refresh completed")))
				output.Reset()
				s.advance(time.Minute)
				synctest.Wait()

				require.False(t, s.refreshRequested)
				require.Equal(t, 2, store.sourceReads)
				require.Equal(t, 1, store.inserts)
				require.Equal(t, 1, store.activations)
				require.Equal(t, 1, regularCleanupAttempts(output.Bytes()))
				require.False(t, s.worker.cleanupDue)
				unchanged := 1
				if failed {
					unchanged = 0
					require.Contains(t, output.String(), `"operation":"refresh"`)
				}
				require.Equal(t, unchanged, bytes.Count(output.Bytes(), []byte("Subscription snapshot source unchanged")))
				require.Zero(t, bytes.Count(output.Bytes(), []byte("Subscription snapshot created")))
				serviceLogMessages(t, output.Bytes())
				s.advance(time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 2, store.sourceReads)
				s.shutdown()
			})
		})
	}
}

// TestQueuedFallbackRefreshPreservesBudgetsAndRetryBlocks checks fresh MongoDB timeouts and retained retry blocks during flapping.
func TestQueuedFallbackRefreshPreservesBudgetsAndRetryBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, time.Hour)
		a := z.nodes[preparedNode].value
		preparedAt := s.worker.publication.candidate.preparedAt
		store.source[0] = sourceDocument(t, "a", "fallback B")
		s.attemptRefresh()
		require.True(t, s.refreshRequested)
		z.beforeWrite = func(ctx context.Context, name string, _ descriptor, _ zNode) (writeOutcome, error) {
			require.Equal(t, activatedNode, name)
			<-ctx.Done()
			return writeUncertain, ctx.Err()
		}
		store.beforeHead = func(ctx context.Context) error {
			require.NoError(t, ctx.Err(), "MongoDB never inherits the expired ZooKeeper context")
			return nil
		}
		store.activation = func(previous string, next head) (bool, error) {
			if next.Version.SnapshotID != a.SnapshotID {
				return false, context.DeadlineExceeded
			}
			return store.apply(previous, next), nil
		}
		s.advance(time.Minute + zooKeeperIOTimeout)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		require.Equal(t, 2, store.activations, "one CAS each for A and B")
		require.NotNil(t, s.worker.proposal)
		require.True(t, s.mongoRetryBlocked)
		require.False(t, s.worker.publication.canRetry())
		require.True(t, sameDescriptor(a, s.worker.publication.candidate.value))
		require.Equal(t, preparedAt, s.worker.publication.candidate.preparedAt)
		for range 3 {
			signalZooKeeper(z, false)
			synctest.Wait()
			signalZooKeeper(z, true)
			synctest.Wait()
		}
		require.Equal(t, 2, store.activations, "continuation and flapping cannot release the MongoDB retry block")
		require.Equal(t, 2, store.sourceReads)
		s.shutdown()
	})
}

// TestCleanupCoalescingRetriesOnlyAtEligibleEvents checks one pending cleanup attempt per refresh or usable recovery event.
func TestCleanupCoalescingRetriesOnlyAtEligibleEvents(t *testing.T) {
	for _, failure := range []string{"deferred", "error"} {
		t.Run(failure, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, store, z := publicationService(t, time.Hour)
				store.source[0] = sourceDocument(t, "a", "next B")
				s.attemptRefresh()
				var output bytes.Buffer
				previous := log.Logger
				log.Logger = zerolog.New(&output)
				t.Cleanup(func() { log.Logger = previous })
				if failure == "error" {
					store.beforeRead = func(context.Context) error {
						store.readError = context.DeadlineExceeded
						return context.DeadlineExceeded
					}
				}
				signalZooKeeper(z, false)
				synctest.Wait()
				require.Equal(t, 1, regularCleanupAttempts(output.Bytes()))
				require.True(t, s.worker.cleanupDue)
				for range 3 {
					signalZooKeeper(z, false)
					synctest.Wait()
				}
				require.Equal(t, 1, regularCleanupAttempts(output.Bytes()), "offline chatter has no separate cleanup entitlement")
				store.beforeRead = nil
				store.readError = nil
				output.Reset()
				s.advance(time.Hour)
				synctest.Wait()
				require.Equal(t, 1, regularCleanupAttempts(output.Bytes()), "the next tick retries once")
				require.True(t, s.worker.cleanupDue)
				output.Reset()
				signalZooKeeper(z, true)
				synctest.Wait()
				require.Equal(t, 1, regularCleanupAttempts(output.Bytes()))
				output.Reset()
				s.advance(time.Minute)
				synctest.Wait()
				require.Equal(t, 1, regularCleanupAttempts(output.Bytes()))
				require.False(t, s.worker.cleanupDue, "only successful cleanup clears the need")
				output.Reset()
				s.attemptRefresh()
				require.Zero(t, regularCleanupAttempts(output.Bytes()), "no cleanup need means no attempt")
				s.shutdown()
			})
		})
	}
}

// TestFollowUpPrepareTimeoutUsesFreshMongoBudget checks immediate fallback after a follow-up preparation timeout.
func TestFollowUpPrepareTimeoutUsesFreshMongoBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, time.Hour)
		a := z.nodes[preparedNode].value
		store.source[0] = sourceDocument(t, "a", "prepare timeout B")
		s.attemptRefresh()
		z.beforeWrite = func(ctx context.Context, name string, value descriptor, _ zNode) (writeOutcome, error) {
			if name == preparedNode && value.SnapshotID != a.SnapshotID {
				<-ctx.Done()
				return writeUncertain, ctx.Err()
			}
			return writeConfirmed, nil
		}
		store.beforeHead = func(ctx context.Context) error {
			require.NoError(t, ctx.Err(), "follow-up fallback has a fresh MongoDB budget")
			return nil
		}
		var output bytes.Buffer
		previous := log.Logger
		log.Logger = zerolog.New(&output)
		t.Cleanup(func() { log.Logger = previous })
		s.advance(time.Minute + zooKeeperIOTimeout)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		require.Equal(t, 2, store.inserts)
		require.Equal(t, 2, store.activations, "B's fallback CAS occurs in the same event as its prepare timeout")
		require.Nil(t, s.worker.proposal)
		require.False(t, s.mongoRetryBlocked)
		candidate := s.worker.publication.candidate
		require.True(t, sameDescriptor(candidate.value, store.current.Version))
		require.True(t, candidate.mongoConfirmed)
		require.True(t, candidate.preparedAt.IsZero())
		require.True(t, candidate.operation.uncertain)
		require.Equal(t, 1, regularCleanupAttempts(output.Bytes()))
		require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("Subscription snapshot created")))
		serviceLogMessages(t, output.Bytes())
		s.shutdown()
	})
}

// TestSnapshotCreationDurationAndCleanupOrder checks stage ordering and durations that exclude unrelated work.
func TestSnapshotCreationDurationAndCleanupOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, z := newFakeStore(t), newFakeZooKeeper()
		c := testConfig()
		c.RefreshInterval = time.Hour
		c.ActivationDelay = 0
		s := configuredPublicationService(t, c, store, z)
		store.source[0] = sourceDocument(t, "a", "timed B")
		store.beforeRead = func(context.Context) error {
			time.Sleep(2 * time.Second)
			return nil
		}
		z.beforeWrite = func(_ context.Context, name string, _ descriptor, _ zNode) (writeOutcome, error) {
			delay := 3 * time.Second
			if name == activatedNode {
				delay = 4 * time.Second
			}
			time.Sleep(delay)
			return writeConfirmed, nil
		}
		var output bytes.Buffer
		previous := log.Logger
		log.Logger = zerolog.New(&output)
		t.Cleanup(func() { log.Logger = previous })
		store.beforeCleanup = func(context.Context) error {
			time.Sleep(5 * time.Second)
			return nil
		}
		start := time.Now()
		s.attemptRefresh()
		require.Equal(t, 14*time.Second, time.Since(start))
		messages := serviceLogMessages(t, output.Bytes())
		require.Equal(t, []string{
			"Subscription snapshot created", "Subscription snapshot ZooKeeper prepared",
			"Subscription snapshot published", "Subscription snapshot ZooKeeper activated",
			"Subscription snapshot cleanup completed",
		}, messageNames(messages))
		require.Equal(t, float64(2000), messages[0]["durationMs"], "exclude publication and cleanup")
		require.Equal(t, float64(3000), messages[1]["durationMs"])
		require.Equal(t, float64(5000), messages[2]["durationMs"])
		require.Equal(t, float64(7000), messages[3]["durationMs"])
		require.Equal(t, float64(5000), messages[4]["durationMs"])
		require.Equal(t, "debug", messages[4]["level"])
		require.Equal(t, float64(0), messages[4]["deletedDocuments"])
		s.shutdown()
	})
}

// TestFollowUpRefreshHasOwnDurationWindow checks that a queued snapshot's creation duration excludes earlier publication work.
func TestFollowUpRefreshHasOwnDurationWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, time.Hour)
		a := z.nodes[preparedNode].value
		store.source[0] = sourceDocument(t, "a", "timed follow-up B")
		s.attemptRefresh()
		store.beforeRead = func(context.Context) error {
			time.Sleep(2 * time.Second)
			return nil
		}
		store.activation = func(previous string, next head) (bool, error) {
			time.Sleep(3 * time.Second)
			return store.apply(previous, next), nil
		}
		z.beforeWrite = func(_ context.Context, name string, _ descriptor, _ zNode) (writeOutcome, error) {
			delay := 3 * time.Second
			if name == activatedNode {
				delay = 4 * time.Second
			}
			time.Sleep(delay)
			return writeConfirmed, nil
		}
		var output bytes.Buffer
		previous := log.Logger
		log.Logger = zerolog.New(&output)
		t.Cleanup(func() { log.Logger = previous })
		s.advance(time.Minute + 12*time.Second)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		require.Equal(t, 1, store.activations)
		require.True(t, sameDescriptor(a, store.current.Version))
		var created []map[string]any
		for _, entry := range serviceLogMessages(t, output.Bytes()) {
			if entry["message"] == "Subscription snapshot created" {
				created = append(created, entry)
			}
		}
		require.Len(t, created, 1)
		require.Equal(t, float64(2000), created[0]["durationMs"], "exclude A's timer, CAS, activation and B's prepare")
		require.Equal(t, 1, regularCleanupAttempts(output.Bytes()))
		require.True(t, sameDescriptor(s.worker.proposal.next.Version, z.nodes[preparedNode].value))
		s.shutdown()
	})
}

// regularCleanupAttempts counts successful, deferred and failed regular cleanup attempts in captured logs.
func regularCleanupAttempts(output []byte) int {
	return bytes.Count(output, []byte(`"operation":"cleanup"`)) +
		bytes.Count(output, []byte(`"message":"Subscription snapshot cleanup completed"`))
}

// messageNames extracts string message fields from decoded log entries in their original order.
func messageNames(entries []map[string]any) []string {
	var names []string
	for _, entry := range entries {
		if message, ok := entry["message"].(string); ok {
			names = append(names, message)
		}
	}
	return names
}
