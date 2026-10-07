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
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func testMongoCrashRecovery(t *testing.T, uri string) {
	for _, phase := range []string{"mid-build", "before activation", "activation acknowledgement lost"} {
		t.Run(phase, func(t *testing.T) {
			client, store, w := mongoFixture(t, uri)
			ctx := t.Context()
			for range 3 {
				require.NoError(t, newWorker(w.config, store).refresh(ctx))
			}
			before, err := store.readHead(ctx)
			require.NoError(t, err)
			var candidate string
			faults := crashFaults(store, phase, &candidate)
			crashed := newWorker(w.config, faults)
			require.Error(t, crashed.refresh(ctx))
			afterCrash, err := store.readHead(ctx)
			require.NoError(t, err)
			if phase == "activation acknowledgement lost" {
				require.Equal(t, candidate, afterCrash.Version.SnapshotID)
				require.Equal(t, before.Version, afterCrash.RecentSnapshots[1])
			} else {
				require.True(t, sameHead(before, afterCrash))
			}
			expectedRows := int64(3)
			if phase == "mid-build" {
				expectedRows = 1
			}
			rows, err := store.countSnapshot(ctx, candidate)
			require.NoError(t, err)
			require.Equal(t, expectedRows, rows)

			// Lose all process-local proposal/orphan state while preserving only MongoDB data.
			reopened := newMongoStore(client, w.config)
			restarted := newWorker(w.config, reopened)
			require.Error(t, restarted.cleanup(ctx))
			require.NoError(t, restarted.refresh(ctx))
			active, err := reopened.readHead(ctx)
			require.NoError(t, err)
			require.NotEqual(t, candidate, active.Version.SnapshotID)
			require.Equal(t, afterCrash.RecentSnapshots[:2], active.RecentSnapshots[1:])
			require.Equal(t, int64(3), active.Version.DocumentCount)
			verifyRecoveredCleanup(t, restarted, reopened, candidate, phase == "activation acknowledgement lost")
		})
	}
}

func crashFaults(store *mongoStore, phase string, candidate *string) *faultStore {
	faults := &faultStore{snapshotStore: store}
	faults.insert = func(ctx context.Context, id string, source *sourceBuffer) error {
		*candidate = id
		if phase == "mid-build" {
			source = &sourceBuffer{documents: source.documents[:1]}
		}
		if err := store.insertSnapshot(ctx, id, source); err != nil {
			return err
		}
		if phase != "activation acknowledgement lost" {
			return context.Canceled
		}
		return nil
	}
	faults.cas = func(ctx context.Context, previous string, next head) (bool, error) {
		matched, err := store.activate(ctx, previous, next)
		if err != nil || !matched {
			return matched, err
		}
		return false, context.DeadlineExceeded
	}
	return faults
}

func verifyRecoveredCleanup(t *testing.T, w *worker, store *mongoStore, candidate string, activated bool) {
	t.Helper()
	ctx := t.Context()
	active, err := store.readHead(ctx)
	require.NoError(t, err)
	rows, err := store.countSnapshot(ctx, candidate)
	require.NoError(t, err)
	if activated {
		require.Equal(t, int64(3), rows, "successfully activated predecessor must stay protected")
	} else {
		require.Zero(t, rows, "unpublished orphan is removed immediately after the new snapshot is activated")
		require.False(t, containsSnapshot(active, candidate))
	}
	for _, version := range active.RecentSnapshots {
		rows, err := store.countSnapshot(ctx, version.SnapshotID)
		require.NoError(t, err)
		require.Equal(t, version.DocumentCount, rows)
	}
}

func testMongoInterruptedCleanup(t *testing.T, uri string) {
	for _, lostAcknowledgement := range []bool{false, true} {
		t.Run(strconv.FormatBool(lostAcknowledgement), func(t *testing.T) {
			_, store, w := mongoFixture(t, uri)
			ctx := t.Context()
			require.NoError(t, w.refresh(ctx))
			before, err := store.readHead(ctx)
			require.NoError(t, err)
			expired := primitive.NewObjectIDFromTimestamp(time.Now().Add(-8 * 24 * time.Hour)).Hex()
			buffer := newSourceBuffer()
			for i := range 2*batchSize + 5 {
				buffer.documents = append(buffer.documents, sourceDocument(t, strconv.Itoa(i), "orphan"))
			}
			require.NoError(t, store.insertSnapshot(ctx, expired, buffer))
			calls := 0
			faults := &faultStore{snapshotStore: store}
			faults.delete = func(ctx context.Context, id string) (int64, error) {
				calls++
				if !lostAcknowledgement && calls == 2 {
					return 0, context.Canceled
				}
				count, err := store.deleteBatch(ctx, id)
				if err == nil && lostAcknowledgement {
					return 0, context.DeadlineExceeded
				}
				return count, err
			}
			w.store = faults
			require.Error(t, w.cleanup(ctx))
			remaining, err := store.countSnapshot(ctx, expired)
			require.NoError(t, err)
			require.Equal(t, int64(batchSize+5), remaining, "first batch deleted, remainder must be retryable")
			afterFailure, err := store.readHead(ctx)
			require.NoError(t, err)
			require.True(t, sameHead(before, afterFailure))
			w.store = store
			require.NoError(t, w.cleanup(ctx))
			remaining, err = store.countSnapshot(ctx, expired)
			require.NoError(t, err)
			require.Zero(t, remaining)
			afterRetry, err := store.readHead(ctx)
			require.NoError(t, err)
			require.True(t, sameHead(before, afterRetry))
			count, err := store.countSnapshot(ctx, before.Version.SnapshotID)
			require.NoError(t, err)
			require.Equal(t, before.Version.DocumentCount, count)
		})
	}
}

func TestMutationAfterSourceScanUsesBufferedDocuments(t *testing.T) {
	ctx := t.Context()
	store := newFakeStore(t)
	original := sourceDocument(t, "a", "initial")
	var published bson.Raw
	faults := &faultStore{snapshotStore: store}
	faults.insert = func(ctx context.Context, id string, source *sourceBuffer) error {
		published = append(bson.Raw(nil), source.documents[0]...)
		store.source[0] = sourceDocument(t, "a", "changed during build")
		return store.insertSnapshot(ctx, id, source)
	}
	w := newWorker(testConfig(), faults)
	require.NoError(t, w.refresh(ctx))
	require.Equal(t, original, published)
	initial := newSourceBuffer()
	require.NoError(t, initial.add(original, 1024))
	require.Equal(t, initial.sourceHash(), store.current.Version.SourceHash)
	oldID := store.current.Version.SnapshotID
	w.store = store
	require.NoError(t, w.refresh(ctx))
	require.NotEqual(t, oldID, store.current.Version.SnapshotID)
	require.NotEqual(t, initial.sourceHash(), store.current.Version.SourceHash)
}

func TestRefreshAndRestart(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(t)
	w := newWorker(testConfig(), store)
	require.NoError(t, w.refresh(ctx))
	first := store.current
	require.NoError(t, w.refresh(ctx))
	require.Equal(t, 1, store.inserts)
	require.Equal(t, 1, store.activations)
	require.True(t, sameHead(first, store.current))
	restarted := newWorker(testConfig(), store)
	require.NoError(t, restarted.refresh(ctx))
	require.NotEqual(t, first.Version.SnapshotID, store.current.Version.SnapshotID)
	require.Equal(t, first.Version, store.current.RecentSnapshots[1])
}

func TestPublicationReason(t *testing.T) {
	tests := []struct {
		name           string
		existing       bool
		restart        bool
		activeCount    int64
		sourceChanged  bool
		cleanupBlocked bool
		want           publicationReason
	}{
		{name: "initial", want: reasonInitial},
		{name: "active count lower", existing: true, activeCount: 0, cleanupBlocked: true, want: reasonSnapshotCountMismatch},
		{name: "active count higher", existing: true, activeCount: 2, cleanupBlocked: true, want: reasonSnapshotCountMismatch},
		{name: "restart unchanged", existing: true, restart: true, activeCount: 1, want: reasonRestart},
		{
			name: "restart with changed source", existing: true, restart: true, activeCount: 1,
			sourceChanged: true, want: reasonRestart,
		},
		{
			name: "restart with incomplete snapshot", existing: true, restart: true, activeCount: 0,
			sourceChanged: true, cleanupBlocked: true, want: reasonSnapshotCountMismatch,
		},
		{name: "source changed", existing: true, activeCount: 1, sourceChanged: true, want: reasonSourceChanged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := newFakeStore(t)
			w := newWorker(testConfig(), store)
			if tt.existing {
				require.NoError(t, w.refresh(ctx))
				store.versions[store.current.Version.SnapshotID] = tt.activeCount
			}
			if tt.restart {
				w = newWorker(testConfig(), store)
			}
			if tt.sourceChanged {
				store.source[0] = sourceDocument(t, "a", "changed")
			}

			var output bytes.Buffer
			previousLogger := log.Logger
			log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
			t.Cleanup(func() { log.Logger = previousLogger })

			assertCleanupResult(t, w.refresh(ctx), tt.cleanupBlocked)
			assertPublicationLogs(t, output.Bytes(), store.current, tt.want, tt.cleanupBlocked)

			output.Reset()
			assertCleanupResult(t, w.refresh(ctx), tt.cleanupBlocked)
			require.Empty(t, output.String(), "unchanged refresh must not log a publication")
		})
	}
}

func assertCleanupResult(t *testing.T, err error, blocked bool) {
	t.Helper()
	if blocked {
		require.ErrorContains(t, err, "protected snapshot is incomplete")
		return
	}
	require.NoError(t, err)
}

func assertPublicationLogs(t *testing.T, output []byte, current head, reason publicationReason, cleanupBlocked bool) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(output), []byte("\n"))
	wantLines := 2
	if cleanupBlocked {
		wantLines = 1
	}
	require.Len(t, lines, wantLines)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &entry), "expected publish log first")
	require.Equal(t, "Subscription snapshot published", entry["message"])
	require.Equal(t, string(reason), entry["snapshotReason"])
	require.Equal(t, current.Version.SnapshotID, entry["snapshotId"])
	require.Equal(t, float64(current.Version.DocumentCount), entry["documentCount"])
	require.Contains(t, entry, "durationMs")
	require.NotContains(t, entry, "startup")
	require.NotContains(t, entry, "sourceHashChanged")
	require.NotContains(t, entry, "activeSnapshotComplete")
}

func TestSourceChangesAndRepair(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *fakeStore)
		count  int64
	}{
		{"insert", func(t *testing.T, f *fakeStore) { f.source = append(f.source, sourceDocument(t, "b", "new")) }, 2},
		{"delete", func(_ *testing.T, f *fakeStore) { f.source = nil }, 0},
		{"same-count update", func(t *testing.T, f *fakeStore) { f.source[0] = sourceDocument(t, "a", "updated") }, 1},
		{"same-count replacement", func(t *testing.T, f *fakeStore) { f.source[0] = sourceDocument(t, "b", "new") }, 1},
		{"repair", func(_ *testing.T, f *fakeStore) { delete(f.versions, f.current.Version.SnapshotID) }, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore(t)
			w := newWorker(testConfig(), store)
			require.NoError(t, w.refresh(context.Background()))
			old := store.current.Version
			tt.mutate(t, store)
			err := w.refresh(context.Background())
			if tt.name == "repair" {
				require.ErrorContains(t, err, "protected snapshot is incomplete")
			} else {
				require.NoError(t, err)
			}
			require.NotEqual(t, old.SnapshotID, store.current.Version.SnapshotID)
			require.Equal(t, tt.count, store.current.Version.DocumentCount)
			require.Equal(t, old, store.current.RecentSnapshots[1])
		})
	}
}

func TestRefreshFailurePreservesHead(t *testing.T) {
	for _, failure := range []string{"head", "source", "oversized", "insert"} {
		t.Run(failure, func(t *testing.T) {
			store := newFakeStore(t)
			w := newWorker(testConfig(), store)
			require.NoError(t, w.refresh(context.Background()))
			old := store.current
			store.source[0] = sourceDocument(t, "a", "changed")
			switch failure {
			case "head":
				store.readError = errors.New("unavailable")
			case "source":
				store.sourceError = errors.New("missing source")
			case "oversized":
				w.config.MaxSnapshotBytes = 1
			case "insert":
				store.insertError = errors.New("failed batch")
			}
			require.Error(t, w.refresh(context.Background()))
			require.True(t, sameHead(old, store.current))
			require.Nil(t, w.proposal)
			if failure == "insert" {
				orphan := w.abandoned
				require.NotEmpty(t, orphan)
				store.insertError = nil
				require.NoError(t, w.refresh(context.Background()))
				require.NotContains(t, store.versions, orphan)
				require.Equal(t, old.Version, store.current.RecentSnapshots[1])
			}
		})
	}
}

func TestUncertainActivation(t *testing.T) {
	for _, applied := range []bool{false, true} {
		name := "old head observed"
		if applied {
			name = "applied without acknowledgement"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := newFakeStore(t)
			w := newWorker(testConfig(), store)
			var candidate head
			store.activation = func(expected string, next head) (bool, error) {
				require.Empty(t, expected, "bootstrap matches absence, represented by an empty expected ID")
				candidate = next
				if applied {
					require.True(t, store.apply(expected, next))
				}
				return false, context.DeadlineExceeded
			}
			require.Error(t, w.refresh(ctx))
			require.NotNil(t, w.proposal)
			require.Error(t, w.cleanup(ctx))
			require.Empty(t, store.deletions)
			store.source[0] = sourceDocument(t, "a", "must not rebuild pending candidate")
			store.activation = nil
			var output bytes.Buffer
			previousLogger := log.Logger
			log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
			t.Cleanup(func() { log.Logger = previousLogger })
			require.NoError(t, w.refresh(ctx))
			lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
			require.Len(t, lines, 2)
			var entry map[string]any
			require.NoError(t, json.Unmarshal(lines[0], &entry))
			require.Equal(t, string(reasonInitial), entry["snapshotReason"])
			require.True(t, sameHead(candidate, store.current))
			require.Equal(t, 1, store.inserts)
			require.Len(t, store.current.RecentSnapshots, 1)
			require.Nil(t, w.proposal)
		})
	}
}

func TestDelayedPreRestartActivation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(t)
	previousProcess := newWorker(testConfig(), store)
	var delayed proposal
	store.activation = func(expected string, next head) (bool, error) {
		delayed = proposal{previous: store.current, next: next}
		require.Empty(t, expected)
		return false, context.DeadlineExceeded
	}
	require.Error(t, previousProcess.refresh(ctx))
	restarted := newWorker(testConfig(), store)
	var candidateID string
	store.activation = func(expected string, next head) (bool, error) {
		candidateID = next.Version.SnapshotID
		require.True(t, store.apply(delayed.previous.Version.SnapshotID, delayed.next))
		require.False(t, store.apply(expected, next))
		return false, nil
	}
	require.Error(t, restarted.refresh(ctx), "old request wins the first CAS")
	require.Error(t, restarted.cleanup(ctx))
	store.activation = nil
	var output bytes.Buffer
	previousLogger := log.Logger
	log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
	t.Cleanup(func() { log.Logger = previousLogger })
	require.NoError(t, restarted.refresh(ctx))
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	require.Len(t, lines, 2)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(lines[0], &entry))
	require.Equal(t, string(reasonInitial), entry["snapshotReason"])
	require.Equal(t, candidateID, store.current.Version.SnapshotID)
	require.Equal(t, delayed.next.Version, store.current.RecentSnapshots[1])
	require.False(t, store.apply(delayed.previous.Version.SnapshotID, delayed.next), "delayed CAS cannot match again")
	require.Equal(t, 2, store.inserts)
}

func TestCleanupRetainsOnlyHeadHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, total := range []int{1, 2, 3, 5} {
		store := newFakeStore(t)
		for i := range total {
			version := testDescriptor(now.Add(-time.Duration(30-i)*24*time.Hour), int64(i%2))
			store.current = proposeHead(store.current, version, 3)
			if version.DocumentCount != 0 {
				store.versions[version.SnapshotID] = version.DocumentCount
			}
		}
		boundary := testDescriptor(now.Add(-168*time.Hour), 1)
		expired := testDescriptor(now.Add(-168*time.Hour-time.Second), 1)
		store.versions[boundary.SnapshotID] = 1
		store.versions[expired.SnapshotID] = 1
		w := newWorker(testConfig(), store)
		w.starting = false
		require.NoError(t, w.cleanup(context.Background()))
		require.NotContains(t, store.versions, boundary.SnapshotID)
		require.NotContains(t, store.versions, expired.SnapshotID)
		for _, version := range store.current.RecentSnapshots {
			require.NotContains(t, store.deletions, version.SnapshotID)
		}
		expectedVersions := 0
		for _, version := range store.current.RecentSnapshots {
			if version.DocumentCount > 0 {
				expectedVersions++
			}
		}
		require.Len(t, store.versions, expectedVersions,
			"only snapshots protected by the active head history should remain")
	}
}

func TestCleanupIntegrityAndHeadRecheck(t *testing.T) {
	for _, scenario := range []string{"malformed ID", "incomplete protected version", "head change"} {
		t.Run(scenario, func(t *testing.T) {
			store := newFakeStore(t)
			w := newWorker(testConfig(), store)
			require.NoError(t, w.refresh(context.Background()))
			old := testDescriptor(time.Now().Add(-30*24*time.Hour), 1)
			store.versions[old.SnapshotID] = 1
			switch scenario {
			case "malformed ID":
				store.versions["invalid"] = 1
			case "incomplete protected version":
				delete(store.versions, store.current.Version.SnapshotID)
			case "head change":
				store.beforeDelete = func() {
					store.current = proposeHead(store.current, testDescriptor(time.Now(), 0), 3)
					store.beforeDelete = nil
				}
			}
			require.Error(t, w.cleanup(context.Background()))
			if scenario == "malformed ID" {
				require.Contains(t, store.versions, "invalid")
			}
		})
	}
}

func TestResetHeadDoesNotLoseHistory(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore(t)
	w := newWorker(testConfig(), store)
	require.NoError(t, w.refresh(ctx))
	store.current = head{ID: "head", RecentSnapshots: []descriptor{}}
	require.ErrorContains(t, w.refresh(ctx), "reset to bootstrap")
	require.ErrorContains(t, w.cleanup(ctx), "reset to bootstrap")
	require.Equal(t, 1, store.inserts)
	require.Empty(t, store.deletions)
}
