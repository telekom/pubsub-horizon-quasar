// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Shopify/zk"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/config"
)

// initialRefreshTestConfig sets a two-minute startup delay with a short, independent refresh timeout.
func initialRefreshTestConfig() config.SubscriptionSnapshots {
	c := testConfig()
	c.InitialRefreshDelay = 2 * time.Minute
	c.RefreshInterval = 5 * time.Minute
	c.RefreshTimeout = time.Second
	return c
}

// TestInitialRefreshDelayZero checks immediate scanning and the normal or fallback activation path.
func TestInitialRefreshDelayZero(t *testing.T) {
	for _, online := range []bool{false, true} {
		name := "offline"
		if online {
			name = "online"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := initialRefreshTestConfig()
				c.InitialRefreshDelay = 0
				store, z := newFakeStore(t), newFakeZooKeeper()
				z.online = online
				s := configuredPublicationService(t, c, store, z)
				require.Equal(t, 1, store.sourceReads)
				require.Equal(t, 1, store.inserts)
				if online {
					require.Equal(t, []string{preparedNode}, z.writes)
					require.Zero(t, store.activations)
				} else {
					require.Empty(t, z.writes)
					require.Equal(t, 1, store.activations)
				}
				s.advance(c.ActivationDelay)
				synctest.Wait()
				require.Equal(t, 1, store.activations)
				if online {
					require.True(t, sameDescriptor(store.current.Version, z.nodes[activatedNode].value))
				}
				s.shutdown()
			})
		})
	}
}

// TestInitialRefreshDelayNormalPublication checks startup waiting and fresh source data on bootstrap and restart.
func TestInitialRefreshDelayNormalPublication(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "bootstrap"
		if existing {
			name = "restart"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := initialRefreshTestConfig()
				c.RefreshInterval = 30 * time.Second
				store, z := newFakeStore(t), newFakeZooKeeper()
				if existing {
					require.NoError(t, newWorker(c, store).refresh(context.Background()))
					z.nodes[preparedNode] = zNode{exists: true, value: store.current.Version, version: 0, czxid: 1}
					z.nodes[activatedNode] = zNode{exists: true, value: store.current.Version, version: 0, czxid: 2}
					store.sourceReads, store.inserts, store.activations, store.cleanups = 0, 0, 0, 0
				}
				before := store.current
				s := configuredPublicationService(t, c, store, z)
				require.Zero(t, store.sourceReads)
				require.Zero(t, store.inserts)
				require.Empty(t, z.writes)

				store.source[0] = sourceDocument(t, "a", "changed during startup delay")
				signalZooKeeper(z, true)
				synctest.Wait()
				s.advance(c.InitialRefreshDelay - time.Nanosecond)
				synctest.Wait()
				require.Zero(t, store.sourceReads, "ticks and usable wakes must not scan before the deadline")
				require.Zero(t, store.inserts)
				require.True(t, sameHead(before, store.current))
				require.Empty(t, z.writes)

				s.advance(time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 1, store.sourceReads)
				require.Equal(t, 1, store.inserts)
				require.Equal(t, []string{preparedNode}, z.writes)
				require.True(t, sameHead(before, store.current))
				next := z.nodes[preparedNode].value
				buffer := newSourceBuffer()
				require.NoError(t, buffer.add(store.source[0], c.MaxSnapshotBytes))
				require.Equal(t, buffer.sourceHash(), next.SourceHash, "scan must include changes made during the delay")
				require.Equal(t, time.Now().UTC(), next.CreatedAt)

				s.advance(c.ActivationDelay - time.Nanosecond)
				synctest.Wait()
				require.Zero(t, store.activations)
				require.True(t, sameHead(before, store.current))
				s.advance(time.Nanosecond)
				synctest.Wait()
				require.Equal(t, 1, store.activations)
				require.True(t, sameDescriptor(next, store.current.Version))
				require.True(t, sameDescriptor(next, z.nodes[activatedNode].value))
				require.Equal(t, []string{preparedNode, activatedNode}, z.writes)
				s.shutdown()
			})
		})
	}
}

// TestInitialRefreshDelayZooKeeperFailures checks that outages preserve startup waiting and catch up without rescanning.
func TestInitialRefreshDelayZooKeeperFailures(t *testing.T) {
	timeout := func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	denied := func(context.Context, string) error { return zk.ErrNoAuth }
	tests := []struct {
		name        string
		duringDelay bool
		online      bool
		waitTimeout bool
		readFailure func(context.Context, string) error
	}{
		{name: "initial missing session"},
		{name: "initial read denied", online: true, readFailure: denied},
		{name: "initial read timeout", online: true, waitTimeout: true, readFailure: timeout},
		{name: "disconnect during wait", duringDelay: true},
		{name: "read denied during wait", duringDelay: true, online: true, readFailure: denied},
		{name: "read timeout during wait", duringDelay: true, online: true, waitTimeout: true, readFailure: timeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				c := initialRefreshTestConfig()
				store, z := newFakeStore(t), newFakeZooKeeper()
				store.beforeRead = func(ctx context.Context) error {
					deadline, ok := ctx.Deadline()
					require.True(t, ok)
					require.Equal(t, c.RefreshTimeout, time.Until(deadline), "fallback needs a fresh MongoDB budget")
					return nil
				}
				if !tt.duringDelay {
					z.online, z.beforeRead = tt.online, tt.readFailure
				}
				start := time.Now()
				s := configuredPublicationService(t, c, store, z)
				require.Zero(t, store.sourceReads)
				require.Zero(t, store.inserts)
				require.Zero(t, store.activations)
				if tt.duringDelay {
					s.advance(30 * time.Second)
					store.source[0] = sourceDocument(t, "a", "changed while ZooKeeper is unavailable")
					z.beforeRead = tt.readFailure
					signalZooKeeper(z, tt.online)
					synctest.Wait()
				}
				s.advance(time.Until(s.initialRefreshDeadline) - time.Nanosecond)
				synctest.Wait()
				require.Zero(t, store.sourceReads, "an outage must not bypass the startup deadline")
				require.Zero(t, store.inserts)
				require.Zero(t, store.activations)
				require.Empty(t, z.writes)
				s.advance(time.Nanosecond)
				synctest.Wait()
				if tt.waitTimeout {
					s.advance(c.RefreshTimeout)
					synctest.Wait()
				}
				require.GreaterOrEqual(t, time.Since(start), c.InitialRefreshDelay)
				require.LessOrEqual(t, time.Since(start), c.InitialRefreshDelay+c.RefreshTimeout)
				require.Equal(t, 1, store.sourceReads)
				require.Equal(t, 1, store.inserts)
				require.Equal(t, 1, store.activations, "after startup waiting, an outage bypasses the activation wait")
				require.Empty(t, z.writes)
				current := store.current

				z.beforeRead = nil
				signalZooKeeper(z, true)
				synctest.Wait()
				require.Equal(t, []string{preparedNode}, z.writes)
				require.True(t, sameDescriptor(current.Version, z.nodes[preparedNode].value))
				require.Equal(t, 1, store.sourceReads, "catch-up must not scan or create another startup snapshot")
				require.Equal(t, 1, store.inserts)
				require.True(t, sameHead(current, store.current))

				s.advance(c.ActivationDelay)
				synctest.Wait()
				require.True(t, sameDescriptor(current.Version, z.nodes[activatedNode].value))
				require.Equal(t, 1, store.activations, "catch-up must not rewrite MongoDB history")
				s.shutdown()
			})
		})
	}
}

// TestInitialRefreshDelayShutdown checks that shutdown interrupts startup waiting without scanning or publishing.
func TestInitialRefreshDelayShutdown(t *testing.T) {
	for _, online := range []bool{false, true} {
		name := "offline"
		if online {
			name = "online"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store, z := newFakeStore(t), newFakeZooKeeper()
				z.online = online
				s := configuredPublicationService(t, initialRefreshTestConfig(), store, z)
				s.advance(time.Minute)
				start := time.Now()
				s.shutdown()
				synctest.Wait()
				require.Less(t, time.Since(start), shutdownTimeout)
				require.Zero(t, store.sourceReads)
				require.Zero(t, store.inserts)
				require.Empty(t, z.writes)
			})
		})
	}
}

// TestInitialRefreshDelayRecoveryBeforeDeadline checks that early recovery still waits for startup and activation delays.
func TestInitialRefreshDelayRecoveryBeforeDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, z := newFakeStore(t), newFakeZooKeeper()
		z.online = false
		c := initialRefreshTestConfig()
		c.InitialRefreshDelay = 90 * time.Second
		s := configuredPublicationService(t, c, store, z)
		s.advance(30 * time.Second)
		store.source[0] = sourceDocument(t, "a", "fresh source after recovery")
		signalZooKeeper(z, true)
		synctest.Wait()
		s.advance(c.InitialRefreshDelay - 30*time.Second - time.Nanosecond)
		synctest.Wait()
		require.Zero(t, store.sourceReads)
		require.Zero(t, store.inserts)
		require.Zero(t, store.activations)
		require.Empty(t, z.writes)
		s.advance(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads)
		require.Equal(t, 1, store.inserts)
		require.Equal(t, []string{preparedNode}, z.writes)
		require.Zero(t, store.activations, "a recovered startup uses the normal activation wait")
		s.advance(c.ActivationDelay)
		synctest.Wait()
		require.Equal(t, 1, store.activations)
		require.True(t, sameDescriptor(store.current.Version, z.nodes[activatedNode].value))
		s.shutdown()
	})
}

// TestInitialRefreshDelayLatestFallbackHead checks that recovery publishes the latest confirmed fallback version.
func TestInitialRefreshDelayLatestFallbackHead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, z := newFakeStore(t), newFakeZooKeeper()
		z.online = false
		c := initialRefreshTestConfig()
		c.RefreshInterval = 45 * time.Second
		s := configuredPublicationService(t, c, store, z)
		s.advance(c.InitialRefreshDelay)
		synctest.Wait()
		require.Equal(t, 1, store.activations)
		store.source[0] = sourceDocument(t, "a", "second fallback version")
		s.advance(c.RefreshInterval)
		synctest.Wait()
		store.source[0] = sourceDocument(t, "a", "latest fallback version")
		s.advance(c.RefreshInterval)
		synctest.Wait()
		require.Equal(t, 3, store.activations)
		current := store.current
		signalZooKeeper(z, true)
		synctest.Wait()
		require.Equal(t, 3, store.sourceReads)
		require.Equal(t, 3, store.inserts)
		require.Equal(t, 3, store.activations)
		require.True(t, sameDescriptor(current.Version, z.nodes[preparedNode].value))
		require.Equal(t, []string{preparedNode}, z.writes)
		s.advance(c.ActivationDelay)
		synctest.Wait()
		require.True(t, sameDescriptor(current.Version, z.nodes[activatedNode].value))
		require.Equal(t, 3, store.activations)
		s.shutdown()
	})
}

// TestInitialRefreshDelayFailedScanAtCoincidentTick checks that coincident timers cause only one failed startup scan.
func TestInitialRefreshDelayFailedScanAtCoincidentTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, z := newFakeStore(t), newFakeZooKeeper()
		store.sourceError = context.DeadlineExceeded
		c := initialRefreshTestConfig()
		c.RefreshInterval = c.InitialRefreshDelay
		s := configuredPublicationService(t, c, store, z)
		s.advance(c.InitialRefreshDelay)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads, "a tick coinciding with the deadline must not duplicate the first scan")
		s.advance(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads, "an expired startup timer must not spin after a failed scan")
		store.sourceError = nil
		s.advance(c.RefreshInterval - time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		require.Equal(t, []string{preparedNode}, z.writes)
		s.advance(c.ActivationDelay)
		synctest.Wait()
		require.Equal(t, 1, store.activations)
		s.shutdown()
	})
}

// TestInitialRefreshDelayExpiredBeforeFirstRefresh checks that a late first scan consumes the startup deadline once.
func TestInitialRefreshDelayExpiredBeforeFirstRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, z := newFakeStore(t), newFakeZooKeeper()
		c := initialRefreshTestConfig()
		s := configuredPublicationService(t, c, store, z)
		done := make(chan struct{})
		s.actions <- func() {
			time.Sleep(c.InitialRefreshDelay)
			s.service.attemptRefresh()
			close(done)
		}
		<-done
		synctest.Wait()
		require.True(t, s.initialRefreshDeadline.IsZero())
		require.False(t, s.refreshRequested, "a late first refresh must consume the startup deadline")
		require.Equal(t, 1, store.sourceReads)
		require.Equal(t, []string{preparedNode}, z.writes)
		s.advance(c.ActivationDelay)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads, "an expired startup deadline must not queue another source scan")
		require.Equal(t, 1, store.inserts)
		require.Equal(t, 1, store.activations)
		s.shutdown()
	})
}

// TestInitialRefreshDelayBoundsFailedFallbackRefreshes checks that disconnect wakes do not retry failed source scans.
func TestInitialRefreshDelayBoundsFailedFallbackRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, z := newFakeStore(t), newFakeZooKeeper()
		z.online = false
		store.sourceError = context.DeadlineExceeded
		c := initialRefreshTestConfig()
		s := configuredPublicationService(t, c, store, z)
		require.Zero(t, store.sourceReads)
		for range 3 {
			signalZooKeeper(z, false)
			synctest.Wait()
		}
		require.Zero(t, store.sourceReads, "repeated disconnects must not start a source scan before the deadline")
		s.advance(c.InitialRefreshDelay)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads, "startup deadline provides only one bounded attempt")
		for range 3 {
			signalZooKeeper(z, false)
			synctest.Wait()
		}
		require.Equal(t, 1, store.sourceReads, "repeated disconnects must not retry a failed source scan")
		s.advance(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 1, store.sourceReads)
		store.sourceError = nil
		s.advance(c.RefreshInterval - c.InitialRefreshDelay)
		synctest.Wait()
		require.Equal(t, 2, store.sourceReads)
		require.Equal(t, 1, store.activations)
		s.shutdown()
	})
}

// TestInitialRefreshDelayPreservesUncertainMongoCAS checks that recovery waits for the unresolved startup head update.
func TestInitialRefreshDelayPreservesUncertainMongoCAS(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store, z := newFakeStore(t), newFakeZooKeeper()
		z.online = false
		store.activation = func(string, head) (bool, error) { return false, context.DeadlineExceeded }
		c := initialRefreshTestConfig()
		s := configuredPublicationService(t, c, store, z)
		require.Nil(t, s.worker.proposal)
		s.advance(c.InitialRefreshDelay)
		synctest.Wait()
		require.NotNil(t, s.worker.proposal)
		signalZooKeeper(z, true)
		synctest.Wait()
		s.advance(time.Minute)
		synctest.Wait()
		require.Equal(t, 1, store.inserts)
		require.Equal(t, 1, store.activations)
		require.Empty(t, z.writes, "an unconfirmed startup head must not become a catch-up target")
		store.activation = nil
		s.advance(c.RefreshInterval - c.InitialRefreshDelay - time.Minute)
		synctest.Wait()
		require.Equal(t, 1, store.inserts)
		require.Equal(t, 2, store.activations)
		require.Equal(t, []string{preparedNode}, z.writes)
		s.advance(c.ActivationDelay)
		synctest.Wait()
		require.True(t, sameDescriptor(store.current.Version, z.nodes[activatedNode].value))
		s.shutdown()
	})
}
