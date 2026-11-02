// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Shopify/zk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/snapshot/mongo"
	"github.com/telekom/quasar/internal/snapshot/state"
	"github.com/telekom/quasar/internal/snapshot/zoo"
	"github.com/telekom/quasar/internal/test"
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	testDatabase  = "quasar_snapshot_service_test"
	testSourceCol = "source"
	testTargetCol = "target"
	waitTimeout   = 30 * time.Second
	waitTick      = 50 * time.Millisecond
)

func TestService_UpdateHead(t *testing.T) {
	entry := func(id string) state.HistoryEntry {
		return state.HistoryEntry{ID: id, DocumentCount: 1, Created: time.Unix(0, 0)}
	}

	tests := []struct {
		name            string
		history         []state.HistoryEntry
		expectedHistory []string
	}{
		{
			name:            "AppendsPreviousHeadToHistory",
			history:         []state.HistoryEntry{},
			expectedHistory: []string{"current"},
		},
		{
			name:            "DropsOldestEntryBeyondThree",
			history:         []state.HistoryEntry{entry("h1"), entry("h2"), entry("h3")},
			expectedHistory: []string{"h2", "h3", "current"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			head := &state.Head{HistoryEntry: entry("current"), History: tt.history}
			token := &state.ResumeToken{Value: "token"}
			before := time.Now()

			(&Service{}).updateHead(head, "next", 42, token)

			assert.Equal(t, "next", head.ID)
			assert.EqualValues(t, 42, head.DocumentCount)
			assert.Same(t, token, head.ResumeToken)
			assert.False(t, head.Created.Before(before))

			var historyIds []string
			for _, h := range head.History {
				historyIds = append(historyIds, h.ID)
			}
			assert.Equal(t, tt.expectedHistory, historyIds)
		})
	}
}

func TestService_ChangesPresent(t *testing.T) {
	tests := []struct {
		name            string
		headToken       *state.ResumeToken
		lastResumeToken *state.ResumeToken
		expected        bool
	}{
		{name: "NoEventReceived", headToken: &state.ResumeToken{Value: "a"}, expected: false},
		{name: "HeadWithoutToken", lastResumeToken: &state.ResumeToken{Value: "a"}, expected: false},
		{name: "SameToken", headToken: &state.ResumeToken{Value: "a"}, lastResumeToken: &state.ResumeToken{Value: "a"}, expected: false},
		{name: "DifferentToken", headToken: &state.ResumeToken{Value: "a"}, lastResumeToken: &state.ResumeToken{Value: "b"}, expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Service{head: &state.Head{ResumeToken: tt.headToken}, lastResumeToken: tt.lastResumeToken}
			assert.Equal(t, tt.expected, s.changesPresent())
		})
	}
}

// TestService_Integration shares one MongoDB replicaset and one ZooKeeper ensemble across all sub-tests.
// Sub-tests run sequentially and reset the znodes they depend on.
func TestService_Integration(t *testing.T) {
	mongoUri := test.SetupMongoReplicaSet(t)
	ensemble := test.SetupZooKeeper(t)

	zooClient, err := zoo.NewClient(ensemble.Addresses, 10*time.Second)
	require.NoError(t, err)
	t.Cleanup(zooClient.Disconnect)

	// The zoo client has no delete, so znodes are reset through a raw connection.
	zkConn, _, err := zk.Connect(ensemble.Addresses, 10*time.Second)
	require.NoError(t, err)
	t.Cleanup(zkConn.Close)

	resetZNodes := func(t *testing.T) {
		t.Helper()
		for _, path := range []string{zoo.ZNodePrepared, zoo.ZNodeActivated} {
			if err := zkConn.Delete(t.Context(), path, -1); !errors.Is(err, zk.ErrNoNode) {
				require.NoError(t, err)
			}
		}
	}

	rawMongo, err := mongodriver.Connect(t.Context(), options.Client().ApplyURI(mongoUri))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawMongo.Disconnect(context.Background()) })
	sourceCol := rawMongo.Database(testDatabase).Collection(testSourceCol)
	targetCol := rawMongo.Database(testDatabase).Collection(testTargetCol)

	countSnapshot := func(t *testing.T, snapshotId string) int64 {
		t.Helper()
		count, err := targetCol.CountDocuments(t.Context(), bson.M{"snapshotId": snapshotId})
		require.NoError(t, err)
		return count
	}

	readHead := func(t *testing.T, path string) state.Head {
		t.Helper()
		bytes, err := zooClient.Read(path)
		require.NoError(t, err)
		var head state.Head
		require.NoError(t, json.Unmarshal(bytes, &head))
		return head
	}

	activatedMatchesPrepared := func() bool {
		prepared, err := zooClient.Read(zoo.ZNodePrepared)
		if err != nil {
			return false
		}
		activated, err := zooClient.Read(zoo.ZNodeActivated)
		return err == nil && string(prepared) == string(activated)
	}

	newMongoClient := func(t *testing.T) *mongo.Client {
		t.Helper()
		client, err := mongo.NewClient(mongoUri, testDatabase, testSourceCol, testTargetCol, time.Second)
		require.NoError(t, err)
		t.Cleanup(client.Close)
		return client
	}

	t.Run("IsActivationPending", func(t *testing.T) {
		tests := []struct {
			name      string
			prepared  string
			activated string
			expected  bool
			expectErr bool
		}{
			{name: "PreparedMissing", expectErr: true},
			{name: "ActivatedMissing", prepared: "a", expected: true},
			{name: "HeadsEqual", prepared: "a", activated: "a", expected: false},
			{name: "HeadsDiffer", prepared: "b", activated: "a", expected: true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				resetZNodes(t)
				if tt.prepared != "" {
					require.NoError(t, zooClient.Write(zoo.ZNodePrepared, []byte(tt.prepared)))
				}
				if tt.activated != "" {
					require.NoError(t, zooClient.Write(zoo.ZNodeActivated, []byte(tt.activated)))
				}

				pending, err := (&Service{zoo: zooClient}).isActivationPending()
				assert.Equal(t, tt.expectErr, err != nil, "error: %v", err)
				assert.Equal(t, tt.expected, pending)
			})
		}
	})

	t.Run("ActivatePrepared", func(t *testing.T) {
		t.Run("CopiesPreparedToActivated", func(t *testing.T) {
			resetZNodes(t)
			require.NoError(t, zooClient.Write(zoo.ZNodePrepared, []byte("prepared")))
			s := &Service{zoo: zooClient, activationPending: true}

			s.activatePrepared(0)

			activated, err := zooClient.Read(zoo.ZNodeActivated)
			require.NoError(t, err)
			assert.Equal(t, "prepared", string(activated))
			assert.False(t, s.activationPending)
		})

		t.Run("KeepsPendingWhenPreparedMissing", func(t *testing.T) {
			resetZNodes(t)
			s := &Service{zoo: zooClient, activationPending: true}

			s.activatePrepared(0)

			ok, err := zooClient.Exists(zoo.ZNodeActivated)
			require.NoError(t, err)
			assert.False(t, ok)
			assert.True(t, s.activationPending)
		})
	})

	t.Run("StartFailsOnCorruptPreparedHead", func(t *testing.T) {
		resetZNodes(t)
		require.NoError(t, zooClient.Write(zoo.ZNodePrepared, []byte("not-json")))

		assert.Error(t, NewService(nil, zooClient, time.Second, 0, time.Second).Start())
	})

	t.Run("Lifecycle", func(t *testing.T) {
		resetZNodes(t)
		require.NoError(t, sourceCol.Drop(t.Context()))
		require.NoError(t, targetCol.Drop(t.Context()))
		_, err := sourceCol.InsertMany(t.Context(), []any{bson.M{"_id": "sub-1"}, bson.M{"_id": "sub-2"}})
		require.NoError(t, err)

		svc := NewService(newMongoClient(t), zooClient, 100*time.Millisecond, 0, 100*time.Millisecond)
		require.NoError(t, svc.Start())
		t.Cleanup(svc.Stop)

		initial := readHead(t, zoo.ZNodePrepared)

		t.Run("InitializesHeadOnFreshStart", func(t *testing.T) {
			assert.NotEmpty(t, initial.ID)
			assert.EqualValues(t, 2, initial.DocumentCount)
			assert.Empty(t, initial.History)
			require.NotNil(t, initial.ResumeToken)
			assert.NotEmpty(t, initial.ResumeToken.Value)
			assert.EqualValues(t, 2, countSnapshot(t, initial.ID))
		})

		t.Run("ActivatesInitialHead", func(t *testing.T) {
			require.Eventually(t, activatedMatchesPrepared, waitTimeout, waitTick)
		})

		t.Run("CreatesSnapshotOnChange", func(t *testing.T) {
			_, err := sourceCol.InsertOne(t.Context(), bson.M{"_id": "sub-3"})
			require.NoError(t, err)

			require.Eventually(t, func() bool { return readHead(t, zoo.ZNodePrepared).ID != initial.ID }, waitTimeout, waitTick)

			next := readHead(t, zoo.ZNodePrepared)
			assert.EqualValues(t, 3, next.DocumentCount)
			assert.NotEqual(t, initial.ResumeToken.Value, next.ResumeToken.Value)
			require.Len(t, next.History, 1)
			assert.Equal(t, initial.ID, next.History[0].ID)
			assert.EqualValues(t, 3, countSnapshot(t, next.ID))

			require.Eventually(t, activatedMatchesPrepared, waitTimeout, waitTick)
		})

		t.Run("CleansUpStaleSnapshots", func(t *testing.T) {
			_, err := targetCol.InsertOne(t.Context(), bson.M{"snapshotId": "stale"})
			require.NoError(t, err)

			require.Eventually(t, func() bool { return countSnapshot(t, "stale") == 0 }, waitTimeout, waitTick)
			assert.EqualValues(t, 2, countSnapshot(t, initial.ID), "snapshots in the history must be retained")
		})

		t.Run("RestartResumesExistingHead", func(t *testing.T) {
			svc.Stop()
			prepared := readHead(t, zoo.ZNodePrepared)
			// Diverging heads simulate a crash between preparation and activation.
			require.NoError(t, zooClient.Write(zoo.ZNodeActivated, []byte("{}")))

			restarted := NewService(newMongoClient(t), zooClient, 100*time.Millisecond, 0, time.Hour)
			require.NoError(t, restarted.Start())
			t.Cleanup(restarted.Stop)

			restarted.headMu.Lock()
			assert.Equal(t, prepared.ID, restarted.head.ID)
			assert.Equal(t, prepared.ResumeToken.Value, restarted.head.ResumeToken.Value)
			restarted.headMu.Unlock()

			require.Eventually(t, activatedMatchesPrepared, waitTimeout, waitTick)
			assert.Equal(t, prepared.ID, readHead(t, zoo.ZNodePrepared).ID, "no snapshot without changes")
		})
	})
}
