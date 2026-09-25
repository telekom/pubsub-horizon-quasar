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

// Scheduling uses only the session context, without issuing MongoDB operations.
type fakeSession struct {
	mongo.Session
}

func runScheduledService(t *testing.T, store *fakeStore, refreshInterval, cleanupInterval time.Duration) *service {
	t.Helper()
	return runScheduledServiceWithDelay(t, store, refreshInterval, cleanupInterval, 0)
}

func runScheduledServiceWithDelay(
	t *testing.T, store *fakeStore, refreshInterval, cleanupInterval, initialStartDelay time.Duration,
) *service {
	t.Helper()
	c := testConfig()
	c.InitialStartDelay = initialStartDelay
	c.RefreshInterval = refreshInterval
	c.CleanupInterval = cleanupInterval
	c.OperationTimeout = 10 * time.Minute
	s := newService(c)
	// Skip connection/setup: this test exercises the real loop with an in-memory store.
	s.client = &mongo.Client{}
	s.store = &mongoStore{}
	s.session = &fakeSession{}
	s.worker = newWorker(c, store)
	go func() {
		defer close(s.done)
		if !s.waitForStart() {
			return
		}
		refresh := time.NewTicker(c.RefreshInterval)
		defer refresh.Stop()
		s.loop(refresh.C)
	}()
	t.Cleanup(s.shutdown)
	synctest.Wait()
	return s
}

func TestSchedulingInitialStartDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledServiceWithDelay(t, store, 30*time.Second, time.Hour, time.Minute)
		require.Zero(t, store.sourceReads)
		require.Zero(t, store.cleanups)

		time.Sleep(time.Minute - time.Nanosecond)
		synctest.Wait()
		require.Zero(t, store.sourceReads, "no snapshot work before the delay expires")

		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads)
		require.Equal(t, 1, store.activations)
		require.Equal(t, 1, store.cleanups)

		time.Sleep(30 * time.Second)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads, "the refresh interval starts after the initial delay")
		s.shutdown()
	})
}

func TestSchedulingCancelsInitialStartDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledServiceWithDelay(t, store, 30*time.Second, time.Hour, time.Minute)
		s.shutdown()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		require.Zero(t, store.sourceReads, "shutdown must prevent a delayed startup scan")
		require.Zero(t, store.cleanups)
	})
}

func TestServiceShutdownDuringInitialStartDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c := testConfig()
		c.InitialStartDelay = time.Minute
		s := newService(c)
		go s.run()

		time.Sleep(time.Minute - time.Nanosecond)
		synctest.Wait()
		require.Nil(t, s.client, "MongoDB must not be initialized during the delay")
		require.Nil(t, s.worker)

		s.shutdown()
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Nil(t, s.client)
	})
}

func TestSchedulingRefreshAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Minute, 2*time.Minute)
		require.Equal(t, 1, store.sourceReads, "startup runs without waiting for a tick")
		require.Equal(t, 1, store.activations)
		require.Equal(t, 1, store.cleanups)

		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		require.Equal(t, 1, store.activations, "unchanged checks must not publish")
		require.Equal(t, 1, store.cleanups, "cleanup is not yet due")

		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 3, store.sourceReads)
		require.Equal(t, 2, store.cleanups, "due cleanup runs even on unchanged source")
		require.Equal(t, 1, store.activations)
		store.source[0] = sourceDocument(t, "a", "changed")

		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.activations)
		s.shutdown()
		reads := store.sourceReads
		time.Sleep(10 * time.Minute)
		synctest.Wait()
		require.Equal(t, reads, store.sourceReads, "no work after shutdown")
	})
}

func TestSchedulingCoalescesTicksWhileRefreshIsRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Minute, 2*time.Minute)
		release := make(chan struct{})
		store.beforeRead = func(ctx context.Context) error {
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		time.Sleep(5 * time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads, "ticks must not launch concurrent scans")
		require.Equal(t, 1, store.cleanups, "cleanup must not overlap the blocked refresh")

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
		s := runScheduledService(t, store, time.Minute, 2*time.Minute)
		var readErr error
		store.beforeRead = func(ctx context.Context) error {
			<-ctx.Done()
			readErr = ctx.Err()
			return readErr
		}
		time.Sleep(time.Minute)
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

func TestSchedulingCleanupWithoutRefreshTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Hour, time.Minute)
		expired := testDescriptor(time.Now().Add(-8*24*time.Hour), 1)
		store.versions[expired.SnapshotID] = 1
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads)
		require.Equal(t, 1, store.activations)
		require.Equal(t, 2, store.cleanups)
		require.NotContains(t, store.versions, expired.SnapshotID)
		s.shutdown()
	})
}

func TestSchedulingCleanupAfterDelayedStartup(t *testing.T) {
	tests := []struct {
		name            string
		refreshInterval time.Duration
		readDuration    time.Duration
	}{
		{"slow startup", time.Hour, 10 * time.Second},
		{"frequent refreshes", 20 * time.Second, 10 * time.Second},
		{"startup exceeds cleanup interval", time.Hour, 70 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeStore(t)
				store.beforeRead = func(context.Context) error {
					time.Sleep(tt.readDuration)
					return nil
				}
				s := runScheduledService(t, store, tt.refreshInterval, time.Minute)
				time.Sleep(tt.readDuration)
				synctest.Wait()
				require.Equal(t, 1, store.cleanups, "startup performs one cleanup")

				time.Sleep(time.Minute - time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 1, store.cleanups, "refreshes must not run cleanup early")
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 2, store.cleanups, "cleanup must run when its interval expires")

				time.Sleep(time.Minute)
				synctest.Wait()
				require.Equal(t, 3, store.cleanups, "subsequent cleanups must stay aligned")
				s.shutdown()
			})
		})
	}
}

func TestSchedulingCleanupRetriesWithoutBusyLoop(t *testing.T) {
	tests := []struct {
		name            string
		refreshInterval time.Duration
		retryAfter      time.Duration
	}{
		{"cleanup timer", time.Hour, time.Minute},
		{"next refresh", 40 * time.Second, 20 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeStore(t)
				s := runScheduledService(t, store, tt.refreshInterval, time.Minute)
				expired := testDescriptor(time.Now().Add(-8*24*time.Hour), 1)
				store.versions[expired.SnapshotID] = 1
				s.worker.store = &faultStore{
					snapshotStore: store,
					delete: func(context.Context, string) (int64, error) {
						return 0, context.DeadlineExceeded
					},
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				require.Equal(t, 2, store.cleanups)
				require.Contains(t, store.versions, expired.SnapshotID)

				time.Sleep(tt.retryAfter - time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 2, store.cleanups, "failed cleanup must not retry in a busy loop")
				s.worker.store = store
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 3, store.cleanups)
				require.NotContains(t, store.versions, expired.SnapshotID)
				s.shutdown()
			})
		})
	}
}

func TestSchedulingCleanupIntervalStartsAfterCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, time.Hour, time.Minute)
		expired := testDescriptor(time.Now().Add(-8*24*time.Hour), 1)
		store.versions[expired.SnapshotID] = 1
		store.beforeDelete = func() { time.Sleep(time.Second) }
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.cleanups)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.NotContains(t, store.versions, expired.SnapshotID)

		time.Sleep(time.Minute - time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 2, store.cleanups, "the interval starts after cleanup finishes")
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 3, store.cleanups)
		s.shutdown()
	})
}

func TestSchedulingFailedRefreshDoesNotPostponeDueCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		s := runScheduledService(t, store, 40*time.Second, time.Minute)
		store.beforeRead = func(context.Context) error {
			time.Sleep(30 * time.Second)
			return context.DeadlineExceeded
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 1, store.cleanups, "cleanup must not overlap the ongoing refresh")
		time.Sleep(10 * time.Second)
		synctest.Wait()
		require.Equal(t, 2, store.cleanups, "due cleanup must run immediately after the failed refresh")
		s.shutdown()
	})
}

func TestSchedulingUnresolvedActivationBlocksCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		store.activation = func(string, head) (bool, error) {
			return false, context.DeadlineExceeded
		}
		s := runScheduledService(t, store, time.Hour, time.Minute)
		require.NotNil(t, s.worker.pending)
		time.Sleep(3 * time.Minute)
		synctest.Wait()
		require.Zero(t, store.cleanups, "independent cleanup ticks must not run with an unresolved activation")
		require.Equal(t, 1, store.inserts)
		store.activation = nil
		time.Sleep(57 * time.Minute)
		synctest.Wait()
		require.Nil(t, s.worker.pending)
		require.Equal(t, 1, store.inserts, "recovery must reuse the exact buffered candidate")
		require.Equal(t, 1, store.cleanups)
		s.shutdown()
	})
}

func TestOperationCompletionLogging(t *testing.T) {
	tests := []struct {
		name     string
		run      func(*service, *fakeStore)
		message  string
		level    string
		duration time.Duration
	}{
		{
			name: "unchanged refresh",
			run: func(s *service, store *fakeStore) {
				store.beforeRead = func(context.Context) error {
					time.Sleep(time.Second)
					return nil
				}
				s.attemptRefresh()
			},
			message:  "Subscription snapshot refresh completed",
			level:    "info",
			duration: time.Second,
		},
		{
			name: "successful cleanup",
			run: func(s *service, store *fakeStore) {
				expired := testDescriptor(time.Now().Add(-8*24*time.Hour), 1)
				store.versions[expired.SnapshotID] = 1
				store.beforeDelete = func() { time.Sleep(time.Second) }
				s.attemptCleanup()
			},
			message:  "Subscription snapshot cleanup completed",
			level:    "info",
			duration: 2 * time.Second,
		},
		{
			name: "failed refresh",
			run: func(s *service, store *fakeStore) {
				store.readError = context.DeadlineExceeded
				s.attemptRefresh()
			},
			message: "Subscription snapshot operation failed",
			level:   "error",
		},
		{
			name: "failed cleanup",
			run: func(s *service, store *fakeStore) {
				store.readError = context.DeadlineExceeded
				s.attemptCleanup()
			},
			message: "Subscription snapshot operation failed",
			level:   "error",
		},
		{
			name: "cleanup awaiting activation",
			run: func(s *service, _ *fakeStore) {
				s.worker.pending = &proposal{}
				s.attemptCleanup()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeStore(t)
				s := runScheduledService(t, store, time.Minute, time.Hour)
				var output bytes.Buffer
				previousLogger := log.Logger
				log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
				t.Cleanup(func() { log.Logger = previousLogger })

				tt.run(s, store)
				s.shutdown()
				if tt.message == "" {
					require.Empty(t, output.String(), "skipped operations must not log completion")
					return
				}
				var entry map[string]any
				require.NoError(t, json.Unmarshal(output.Bytes(), &entry), "expected exactly one log event")
				require.Equal(t, tt.message, entry["message"])
				require.Equal(t, tt.level, entry["level"])
				require.Equal(t, float64(tt.duration)/float64(time.Millisecond), entry["durationMs"])
				require.NotContains(t, entry, "duration")
				require.Equal(t, s.config.SourceCollection, entry["sourceCollection"])
				require.Equal(t, s.config.SnapshotCollection, entry["snapshotCollection"])
				require.Equal(t, s.config.HeadCollection, entry["headCollection"])
			})
		})
	}
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

		var entry map[string]any
		require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
		require.Equal(t, "Subscription snapshot published", entry["message"])
		require.Equal(t, float64(1000), entry["durationMs"])
		require.NotContains(t, entry, "duration")
		require.Equal(t, store.current.Version.SnapshotID, entry["snapshotId"])
		require.Equal(t, "initial", entry["snapshotReason"])
	})
}
