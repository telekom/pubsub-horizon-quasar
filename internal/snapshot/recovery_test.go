// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type faultStore struct {
	snapshotStore
	insert func(context.Context, string, *sourceBuffer) error
	cas    func(context.Context, string, head) (bool, error)
	delete func(context.Context, string) (int64, error)
}

func (f *faultStore) insertSnapshot(ctx context.Context, id string, source *sourceBuffer) error {
	if f.insert != nil {
		return f.insert(ctx, id, source)
	}
	return f.snapshotStore.insertSnapshot(ctx, id, source)
}

func (f *faultStore) activate(ctx context.Context, previous string, next head) (bool, error) {
	if f.cas != nil {
		return f.cas(ctx, previous, next)
	}
	return f.snapshotStore.activate(ctx, previous, next)
}

func (f *faultStore) deleteBatch(ctx context.Context, id string) (int64, error) {
	if f.delete != nil {
		return f.delete(ctx, id)
	}
	return f.snapshotStore.deleteBatch(ctx, id)
}

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
			require.Error(t, restarted.cleanup(ctx, time.Now().Add(8*24*time.Hour)))
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
	require.NoError(t, w.cleanup(ctx, time.Now()), "young orphan rows must not be mistaken for old history")
	rows, err := store.countSnapshot(ctx, candidate)
	require.NoError(t, err)
	require.Positive(t, rows)
	require.NoError(t, w.cleanup(ctx, time.Now().Add(8*24*time.Hour)))
	rows, err = store.countSnapshot(ctx, candidate)
	require.NoError(t, err)
	if activated {
		require.Equal(t, int64(3), rows, "successfully activated predecessor must stay protected")
	} else {
		require.Zero(t, rows, "unpublished orphan is removed only after retention expires")
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
			require.Error(t, w.cleanup(ctx, time.Now()))
			remaining, err := store.countSnapshot(ctx, expired)
			require.NoError(t, err)
			require.Equal(t, int64(batchSize+5), remaining, "first batch deleted, remainder must be retryable")
			afterFailure, err := store.readHead(ctx)
			require.NoError(t, err)
			require.True(t, sameHead(before, afterFailure))
			w.store = store
			require.NoError(t, w.cleanup(ctx, time.Now()))
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
