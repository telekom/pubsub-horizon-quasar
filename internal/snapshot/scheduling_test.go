// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
)

type fakeSession struct {
	mongo.Session
}

type scheduledService struct {
	*service
	actions chan func()
}

func (s *scheduledService) advance(duration time.Duration) {
	// Wait acquires worker activity; this channel also orders assertions/mutations before future timer work.
	s.actions <- func() {}
	time.Sleep(duration)
}

func (s *scheduledService) attemptRefresh() {
	done := make(chan struct{})
	s.actions <- func() {
		s.service.attemptRefresh()
		close(done)
	}
	<-done
	synctest.Wait()
}

func runScheduledService(t *testing.T, store *fakeStore, refreshInterval time.Duration) *scheduledService {
	t.Helper()
	c := testConfig()
	c.RefreshInterval = refreshInterval
	c.RefreshTimeout = 10 * time.Minute
	c.CleanupTimeout = 10 * time.Minute
	s := &scheduledService{service: newService(c), actions: make(chan func(), 1)}
	s.client = &mongo.Client{}
	s.store = &mongoStore{}
	s.session = &fakeSession{}
	s.worker = newWorker(c, store)
	go func() {
		defer close(s.done)
		refresh := time.NewTicker(c.RefreshInterval)
		defer refresh.Stop()
		s.loop(refresh.C, s.actions)
	}()
	t.Cleanup(s.shutdown)
	synctest.Wait()
	return s
}

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

func TestSchedulingUnresolvedActivationBlocksCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		store.activation = func(string, head) (bool, error) {
			return false, context.DeadlineExceeded
		}
		s := runScheduledService(t, store, time.Minute)
		require.NotNil(t, s.worker.pending)
		require.Zero(t, store.cleanups)
		require.Equal(t, 1, store.inserts)

		store.activation = nil
		s.advance(time.Minute)
		synctest.Wait()
		require.Nil(t, s.worker.pending)
		require.Equal(t, 1, store.inserts, "recovery must reuse the exact buffered candidate")
		require.Equal(t, 1, store.cleanups, "cleanup follows confirmed activation")
		s.shutdown()
	})
}

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
		require.Len(t, lines, 2)
		var entry map[string]any
		require.NoError(t, json.Unmarshal(lines[1], &entry))
		require.Equal(t, "cleanup", entry["operation"])
		require.True(t, s.worker.cleanupDue)

		output.Reset()
		s.worker.store = store
		s.attemptRefresh()
		require.False(t, s.worker.cleanupDue)
		require.NotContains(t, store.versions, orphan.SnapshotID)
		s.shutdown()
	})
}

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

func TestPublicationLoggingDurationMs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		store.beforeRead = func(context.Context) error {
			time.Sleep(time.Second)
			return nil
		}
		var output bytes.Buffer
		previousLogger := log.Logger
		log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
		t.Cleanup(func() { log.Logger = previousLogger })

		require.NoError(t, newWorker(testConfig(), store).refresh(t.Context()))

		lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
		require.Len(t, lines, 2, "publication and immediate cleanup each emit one log event")
		var publication map[string]any
		require.NoError(t, json.Unmarshal(lines[0], &publication))
		require.Equal(t, "Subscription snapshot published", publication["message"])
		require.Equal(t, float64(1000), publication["durationMs"])
		require.NotContains(t, publication, "duration")
		require.Equal(t, store.current.Version.SnapshotID, publication["snapshotId"])
		require.Equal(t, "initial", publication["snapshotReason"])
		var cleanup map[string]any
		require.NoError(t, json.Unmarshal(lines[1], &cleanup))
		require.Equal(t, "Subscription snapshot cleanup completed", cleanup["message"])
	})
}
