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
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Shopify/zk"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo"
)

type fakeZooKeeper struct {
	online      bool
	session     int64
	nodes       map[string]zNode
	wake        chan struct{}
	writes      []string
	beforeRead  func(context.Context, string) error
	beforeWrite func(context.Context, string, descriptor, zNode) (writeOutcome, error)
}

func newFakeZooKeeper() *fakeZooKeeper {
	return &fakeZooKeeper{online: true, session: 1, nodes: make(map[string]zNode), wake: make(chan struct{}, 1)}
}

func (f *fakeZooKeeper) available() bool             { return f.online }
func (f *fakeZooKeeper) events() <-chan struct{}     { return f.wake }
func (f *fakeZooKeeper) close(context.Context) error { return nil }

func (f *fakeZooKeeper) read(ctx context.Context, name string) (zNode, error) {
	if !f.online {
		return zNode{}, zooKeeperError("read", errZooKeeperUnavailable)
	}
	if f.beforeRead != nil {
		if err := f.beforeRead(ctx, name); err != nil {
			return zNode{}, err
		}
	}
	node := f.nodes[name]
	node.session = f.session
	return node, nil
}

func (f *fakeZooKeeper) write(ctx context.Context, name string, value descriptor, expected zNode) (zNode, writeOutcome, error) {
	if !f.online {
		return zNode{}, writeNotExecuted, zooKeeperError("write", errZooKeeperUnavailable)
	}
	if f.beforeWrite != nil {
		outcome, err := f.beforeWrite(ctx, name, value, expected)
		if err != nil {
			return zNode{}, outcome, err
		}
	}
	node, err := f.apply(name, value, expected)
	if err != nil {
		return zNode{}, writeNotExecuted, err
	}
	return node, writeConfirmed, nil
}

func (f *fakeZooKeeper) apply(name string, value descriptor, expected zNode) (zNode, error) {
	current := f.nodes[name]
	if !sameNode(expected, current) {
		return zNode{}, zk.ErrBadVersion
	}
	next := zNode{exists: true, value: value, session: f.session}
	if current.exists {
		next.czxid, next.version = current.czxid, current.version+1
	} else {
		next.czxid = int64(len(f.writes) + 1)
	}
	f.nodes[name] = next
	f.writes = append(f.writes, name)
	return next, nil
}

func publicationService(t *testing.T, interval time.Duration) (*scheduledService, *fakeStore, *fakeZooKeeper) {
	t.Helper()
	store := newFakeStore(t)
	transport := newFakeZooKeeper()
	c := testConfig()
	c.RefreshInterval = interval
	c.RefreshTimeout = 30 * time.Second
	s := &scheduledService{service: newService(c), actions: make(chan func(), 1)}
	s.client, s.store, s.session = &mongo.Client{}, &mongoStore{}, &fakeSession{}
	s.worker = newWorker(c, store)
	s.zooKeeper = transport
	s.worker.publication = newPublication(transport, c.ActivationDelay)
	go func() {
		defer close(s.done)
		refresh := time.NewTicker(interval)
		defer refresh.Stop()
		s.loop(refresh.C, s.actions)
	}()
	t.Cleanup(s.shutdown)
	synctest.Wait()
	return s, store, transport
}

func signalZooKeeper(f *fakeZooKeeper, online bool) {
	f.online = online
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func TestPublicationNormalTimerAndOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 5*time.Minute)
		require.Len(t, z.writes, 1)
		require.Equal(t, "prepared", z.writes[0])
		require.Empty(t, store.current.Version.SnapshotID)
		require.Zero(t, store.activations)
		require.Equal(t, 1, store.inserts)
		require.Zero(t, store.cleanups)
		candidate := z.nodes["prepared"].value
		s.advance(time.Minute - time.Nanosecond)
		synctest.Wait()
		require.Zero(t, store.activations)
		require.False(t, z.nodes["activated"].exists)
		z.beforeWrite = func(_ context.Context, name string, value descriptor, _ zNode) (writeOutcome, error) {
			if name == "activated" {
				require.True(t, sameDescriptor(value, store.current.Version), "MongoDB must already be confirmed")
				require.False(t, s.worker.lastSuccess.IsZero())
			}
			return writeConfirmed, nil
		}
		s.advance(time.Nanosecond)
		synctest.Wait()
		require.True(t, sameDescriptor(candidate, store.current.Version))
		require.True(t, sameDescriptor(candidate, z.nodes["activated"].value))
		require.Equal(t, []string{"prepared", "activated"}, z.writes)
		require.Equal(t, 1, store.cleanups)
		require.Nil(t, s.worker.pending)
		require.Nil(t, s.worker.publication.pending)
		s.shutdown()
	})
}

func TestPublicationDegradedLatestStateAndFixedCatchUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 2*time.Minute)
		s.advance(time.Minute)
		synctest.Wait()
		a := store.current.Version
		signalZooKeeper(z, false)
		synctest.Wait()
		for i := range 5 {
			store.source[0] = sourceDocument(t, "a", string(rune('b'+i)))
			s.advance(2 * time.Minute)
			synctest.Wait()
			require.NotEqual(t, a.SnapshotID, store.current.Version.SnapshotID)
		}
		f := store.current.Version
		inserts, cas := store.inserts, store.activations
		require.Equal(t, []string{"prepared", "activated"}, z.writes)
		require.True(t, sameDescriptor(a, z.nodes["activated"].value))
		z.session++
		signalZooKeeper(z, true)
		synctest.Wait()
		require.True(t, sameDescriptor(f, z.nodes["prepared"].value))
		require.Equal(t, inserts, store.inserts, "reconnect must not scan or create a snapshot")
		s.advance(time.Minute - time.Nanosecond)
		synctest.Wait()
		require.True(t, sameDescriptor(a, z.nodes["activated"].value))
		s.advance(time.Nanosecond)
		synctest.Wait()
		require.True(t, sameDescriptor(f, z.nodes["activated"].value))
		require.Equal(t, cas, store.activations, "catch-up must not modify MongoDB history")
		require.False(t, s.worker.publication.degraded)
		s.shutdown()
	})
}

func TestPublicationDisconnectDuringWaitingAndNewHead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 20*time.Second)
		b := z.nodes["prepared"].value
		preparedAt := s.worker.publication.pending.preparedAt
		signalZooKeeper(z, false)
		synctest.Wait()
		require.True(t, sameDescriptor(b, store.current.Version), "fallback must not wait for activation delay")
		store.source[0] = sourceDocument(t, "a", "newer C")
		s.advance(20 * time.Second)
		synctest.Wait()
		c := store.current.Version
		require.NotEqual(t, b.SnapshotID, c.SnapshotID)
		require.Equal(t, preparedAt, s.worker.publication.pending.preparedAt)
		z.session++
		signalZooKeeper(z, true)
		synctest.Wait()
		s.advance(40 * time.Second)
		synctest.Wait()
		require.True(t, sameDescriptor(b, z.nodes["activated"].value))
		require.True(t, sameDescriptor(c, store.current.Version), "finishing B must never roll MongoDB back")
		require.True(t, sameDescriptor(c, z.nodes["prepared"].value))
		store.source[0] = sourceDocument(t, "a", "newer G")
		s.advance(20 * time.Second)
		synctest.Wait()
		g := store.current.Version
		require.NotEqual(t, c.SnapshotID, g.SnapshotID)
		require.True(t, sameDescriptor(c, s.worker.publication.pending.value))
		s.advance(40 * time.Second)
		synctest.Wait()
		require.True(t, sameDescriptor(c, z.nodes["activated"].value))
		require.True(t, sameDescriptor(g, z.nodes["prepared"].value))
		require.True(t, sameDescriptor(g, store.current.Version))
		s.shutdown()
	})
}

func TestPublicationUncertainWritesReadBackAndOriginalCAS(t *testing.T) {
	for _, name := range []string{"prepared", "activated"} {
		for _, applied := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/queued", true: "/applied"}[applied], func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					testUncertainZooKeeperWrite(t, name, applied)
				})
			})
		}
	}
}

func testUncertainZooKeeperWrite(t *testing.T, name string, applied bool) {
	t.Helper()
	store := newFakeStore(t)
	z := newFakeZooKeeper()
	w := newWorker(testConfig(), store)
	w.publication = newPublication(z, time.Minute)
	require.NoError(t, w.refreshSnapshot(t.Context()))
	var original zNode
	z.beforeWrite = func(_ context.Context, node string, value descriptor, expected zNode) (writeOutcome, error) {
		if node != name {
			return writeConfirmed, nil
		}
		original = expected
		if applied {
			_, err := z.apply(node, value, expected)
			require.NoError(t, err)
		}
		return writeUncertain, context.DeadlineExceeded
	}
	if name == "activated" {
		require.NoError(t, w.progressZooKeeper(t.Context()))
		time.Sleep(time.Minute)
		require.NoError(t, w.resolve(t.Context()))
	}
	require.ErrorIs(t, w.progressZooKeeper(t.Context()), context.DeadlineExceeded)
	w.publication.degrade(context.DeadlineExceeded)
	candidate := w.publication.pending.value
	if w.pending != nil {
		require.NoError(t, w.resolve(t.Context()))
	}
	success := w.lastSuccess
	z.session++
	z.beforeWrite = func(_ context.Context, node string, _ descriptor, expected zNode) (writeOutcome, error) {
		if node == name {
			require.True(t, sameNode(original, expected), "original CAS must survive session changes")
		}
		return writeConfirmed, nil
	}
	require.NoError(t, w.progressZooKeeper(t.Context()))
	if name == "prepared" {
		require.Equal(t, time.Now(), w.publication.pending.preparedAt)
		time.Sleep(time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
	}
	require.True(t, sameDescriptor(candidate, z.nodes["activated"].value))
	require.Equal(t, success, w.lastSuccess, "ZooKeeper success must not update MongoDB success time")
	require.Equal(t, []string{"prepared", "activated"}, z.writes)
}

func TestPublicationCleanupAndIntegrityRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, time.Minute)
		s.advance(time.Minute)
		synctest.Wait()
		a := z.nodes["activated"]
		signalZooKeeper(z, false)
		synctest.Wait()
		for i := range 5 {
			store.source[0] = sourceDocument(t, "a", string(rune('b'+i)))
			s.advance(time.Minute)
			synctest.Wait()
		}
		require.False(t, containsSnapshot(store.current, a.value.SnapshotID))
		require.Contains(t, store.versions, a.value.SnapshotID)
		require.True(t, s.worker.cleanupDue)
		latest := store.current.Version
		signalZooKeeper(z, true)
		synctest.Wait()
		s.advance(time.Minute)
		synctest.Wait()
		require.True(t, sameDescriptor(latest, z.nodes["activated"].value))
		require.False(t, s.worker.cleanupDue)
		require.NotContains(t, store.versions, a.value.SnapshotID)

		known := z.nodes["activated"]
		recreated := known
		recreated.czxid++
		z.nodes["activated"] = recreated
		s.worker.cleanupDue = true
		store.source[0] = sourceDocument(t, "a", "after corruption")
		s.advance(time.Minute)
		synctest.Wait()
		require.NotEqual(t, latest.SnapshotID, store.current.Version.SnapshotID)
		require.True(t, s.worker.cleanupDue)
		require.True(t, s.worker.publication.degraded)
		require.True(t, sameDescriptor(known.value, z.nodes["activated"].value))
		z.nodes["activated"] = known
		s.advance(time.Minute)
		synctest.Wait()
		s.advance(time.Minute)
		synctest.Wait()
		require.False(t, s.worker.publication.degraded, "restored known state must recover without restart")
		s.shutdown()
	})
}

func TestPublicationPrepareTimeoutUsesFreshMongoBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		z := newFakeZooKeeper()
		c := testConfig()
		c.RefreshTimeout = 30 * time.Second
		s := newService(c)
		s.client, s.store, s.session = &mongo.Client{}, &mongoStore{}, &fakeSession{}
		s.worker = newWorker(c, store)
		s.worker.publication = newPublication(z, time.Minute)
		z.beforeWrite = func(ctx context.Context, _ string, _ descriptor, _ zNode) (writeOutcome, error) {
			<-ctx.Done()
			return writeUncertain, ctx.Err()
		}
		store.beforeHead = func(ctx context.Context) error {
			require.NoError(t, ctx.Err())
			return nil
		}
		start := time.Now()
		s.attemptRefresh()
		require.Equal(t, 5*time.Second, time.Since(start))
		require.Equal(t, 1, store.activations)
		require.NotNil(t, s.worker.publication.pending)
		require.Nil(t, s.worker.pending)
		s.cancel()
	})
}

func TestPublicationShutdownAndRetryScheduling(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 5*time.Minute)
		z.beforeWrite = func(context.Context, string, descriptor, zNode) (writeOutcome, error) {
			return writeUncertain, context.DeadlineExceeded
		}
		s.advance(time.Minute)
		synctest.Wait()
		require.Equal(t, 1, store.activations)
		s.advance(3 * time.Minute)
		synctest.Wait()
		require.Len(t, z.writes, 1, "expired activation timer must not busy-loop")
		z.beforeWrite = nil
		s.advance(time.Minute)
		synctest.Wait()
		require.Len(t, z.writes, 2, "refresh tick retries unchanged source")
		store.source[0] = sourceDocument(t, "a", "shutdown")
		s.advance(5 * time.Minute)
		synctest.Wait()
		before := z.nodes["activated"]
		start := time.Now()
		s.shutdown()
		synctest.Wait()
		require.Less(t, time.Since(start), shutdownTimeout)
		time.Sleep(time.Minute)
		require.True(t, sameNode(before, z.nodes["activated"]))
	})
}

func TestPublicationStartupAbandonsOldPrepare(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		old := newWorker(testConfig(), store)
		require.NoError(t, old.refresh(t.Context()))
		a := store.current.Version
		require.NoError(t, newWorker(testConfig(), store).refresh(t.Context()))
		b := store.current.Version
		z := newFakeZooKeeper()
		z.nodes["prepared"] = zNode{exists: true, version: 1, czxid: 1, value: b}
		z.nodes["activated"] = zNode{exists: true, version: 0, czxid: 2, value: a}
		w := newWorker(testConfig(), store)
		w.publication = newPublication(z, time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.Empty(t, z.writes, "startup must not release the old MongoDB head")
		require.NoError(t, w.refreshSnapshot(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		c := w.pending.next.Version
		require.NotEqual(t, b.SnapshotID, c.SnapshotID)
		require.True(t, sameDescriptor(c, z.nodes["prepared"].value))
		require.True(t, sameDescriptor(a, z.nodes["activated"].value))
		time.Sleep(time.Minute)
		require.NoError(t, w.resolve(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.True(t, sameDescriptor(c, z.nodes["activated"].value))
	})
}

func TestWriteOutcomeClassification(t *testing.T) {
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded, zk.ErrClosing, zk.ErrConnectionClosed,
		zk.ErrSessionExpired, zk.ErrSessionMoved, errors.New("network failure"),
	} {
		require.Equal(t, writeUncertain, classifyWriteOutcome(err))
	}
	for _, err := range []error{zk.ErrBadVersion, zk.ErrNodeExists, zk.ErrNoNode, zk.ErrNoAuth, zk.ErrInvalidACL} {
		require.Equal(t, writeNotExecuted, classifyWriteOutcome(err))
	}
}

func TestPublicationDelayedPreviousProcessWrites(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		require.NoError(t, newWorker(testConfig(), store).refreshSnapshot(t.Context()))
		a := store.current.Version
		require.NoError(t, newWorker(testConfig(), store).refreshSnapshot(t.Context()))
		b := store.current.Version
		z := newFakeZooKeeper()
		z.nodes[preparedNode] = zNode{exists: true, version: 7, czxid: 1, value: b}
		z.nodes[activatedNode] = zNode{exists: true, version: 5, czxid: 2, value: a}
		oldPrepared, oldActivated := z.nodes[preparedNode], z.nodes[activatedNode]
		w := newWorker(testConfig(), store)
		w.publication = newPublication(z, time.Minute)
		require.NoError(t, w.refreshSnapshot(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		c := w.pending.next.Version
		_, err := z.apply(preparedNode, b, oldPrepared)
		require.ErrorIs(t, err, zk.ErrBadVersion, "late prepare must not replace startup C")
		time.Sleep(time.Minute)
		require.NoError(t, w.resolve(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		_, err = z.apply(activatedNode, b, oldActivated)
		require.ErrorIs(t, err, zk.ErrBadVersion, "late activation must not replace released C")
		require.True(t, sameDescriptor(c, store.current.Version))
		require.True(t, sameDescriptor(c, z.nodes[preparedNode].value))
		require.True(t, sameDescriptor(c, z.nodes[activatedNode].value))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.Equal(t, []string{preparedNode, activatedNode}, z.writes)
	})
}

func TestPublicationRejectedRetryKeepsUncertainOriginalWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		z := newFakeZooKeeper()
		w := newWorker(testConfig(), store)
		w.publication = newPublication(z, time.Minute)
		require.NoError(t, w.refreshSnapshot(t.Context()))
		z.beforeWrite = func(context.Context, string, descriptor, zNode) (writeOutcome, error) {
			return writeUncertain, context.DeadlineExceeded
		}
		require.ErrorIs(t, w.progressZooKeeper(t.Context()), context.DeadlineExceeded)
		original := *w.publication.pending.operation
		b := w.publication.pending.value
		w.publication.degrade(context.DeadlineExceeded)
		require.NoError(t, w.resolve(t.Context()))
		store.source[0] = sourceDocument(t, "a", "newer confirmed head")
		require.NoError(t, w.refreshSnapshot(t.Context()))
		require.NoError(t, w.resolve(t.Context()))
		latest := store.current.Version
		z.beforeWrite = func(context.Context, string, descriptor, zNode) (writeOutcome, error) {
			return writeNotExecuted, zk.ErrBadVersion
		}
		require.ErrorIs(t, w.progressZooKeeper(t.Context()), zk.ErrBadVersion)
		require.True(t, w.publication.pending.operation.uncertain)
		require.True(t, sameDescriptor(b, w.publication.pending.value))
		require.True(t, sameNode(original.expected, w.publication.pending.operation.expected))
		require.ErrorIs(t, w.cleanupPending(t.Context()), errCleanupDeferred)
		require.Empty(t, store.deletions)
		_, err := z.apply(preparedNode, b, original.expected)
		require.NoError(t, err, "original request can still execute after retry rejection")
		z.session++
		z.beforeWrite = nil
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.Equal(t, time.Now(), w.publication.pending.preparedAt)
		time.Sleep(time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.True(t, sameDescriptor(b, z.nodes[activatedNode].value))
		require.True(t, sameDescriptor(latest, z.nodes[preparedNode].value))
		require.True(t, sameDescriptor(latest, store.current.Version))
	})
}

func TestPublicationAccessRecoveryWithoutWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 5*time.Minute)
		b := z.nodes[preparedNode].value
		z.beforeRead = func(context.Context, string) error { return zk.ErrNoAuth }
		s.advance(time.Minute)
		synctest.Wait()
		require.True(t, sameDescriptor(b, store.current.Version))
		require.False(t, z.nodes[activatedNode].exists)
		require.ErrorIs(t, s.worker.cleanupPending(t.Context()), errCleanupDeferred)
		store.source[0] = sourceDocument(t, "a", "newer while access denied")
		s.advance(4 * time.Minute)
		synctest.Wait()
		latest := store.current.Version
		require.NotEqual(t, b.SnapshotID, latest.SnapshotID)
		activations, inserts := store.activations, store.inserts
		z.beforeRead = nil
		s.advance(5 * time.Minute)
		synctest.Wait()
		require.True(t, sameDescriptor(b, z.nodes[activatedNode].value))
		require.True(t, sameDescriptor(latest, z.nodes[preparedNode].value))
		s.advance(time.Minute)
		synctest.Wait()
		require.True(t, sameDescriptor(latest, z.nodes[activatedNode].value))
		require.False(t, s.worker.publication.degraded)
		require.Equal(t, activations, store.activations)
		require.Equal(t, inserts, store.inserts, "access repair is not a new MongoDB publication")
		s.shutdown()
	})
}

func TestPublicationMongoFailureAndFlappingAreRateLimited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 5*time.Minute)
		preparedAt := s.worker.publication.pending.preparedAt
		store.activation = func(string, head) (bool, error) { return false, context.DeadlineExceeded }
		s.advance(time.Minute)
		synctest.Wait()
		require.Equal(t, 1, store.activations)
		require.False(t, z.nodes[activatedNode].exists, "unconfirmed MongoDB must not be released")
		for range 10 {
			signalZooKeeper(z, false)
			synctest.Wait()
			z.session++
			signalZooKeeper(z, true)
			synctest.Wait()
		}
		require.Equal(t, 1, store.activations, "connection chatter must not retry MongoDB CAS")
		require.Len(t, z.writes, 1)
		require.Equal(t, preparedAt, s.worker.publication.pending.preparedAt)
		store.activation = nil
		s.advance(4 * time.Minute)
		synctest.Wait()
		require.Equal(t, 2, store.activations)
		require.True(t, sameDescriptor(store.current.Version, z.nodes[activatedNode].value))
		require.False(t, s.worker.publication.degraded)
		require.Equal(t, 1, store.inserts)
		s.shutdown()
	})
}

func TestPublicationAllDeletionPathsProtectExternalHistoryReferences(t *testing.T) {
	for _, name := range []string{preparedNode, activatedNode} {
		t.Run(name, func(t *testing.T) {
			store := newFakeStore(t)
			require.NoError(t, newWorker(testConfig(), store).refreshSnapshot(t.Context()))
			old := store.current.Version
			for range 4 {
				require.NoError(t, newWorker(testConfig(), store).refreshSnapshot(t.Context()))
			}
			require.False(t, containsSnapshot(store.current, old.SnapshotID))
			z := newFakeZooKeeper()
			z.nodes[preparedNode] = zNode{exists: true, czxid: 1, value: store.current.Version}
			z.nodes[activatedNode] = zNode{exists: true, czxid: 2, value: store.current.Version}
			node := z.nodes[name]
			node.value = old
			z.nodes[name] = node
			w := newWorker(testConfig(), store)
			w.starting = false
			w.publication = newPublication(z, time.Minute)
			w.publication.firstActivated = true
			w.publication.latest = store.current.Version
			require.ErrorIs(t, w.deleteVersion(t.Context(), store.current, old.SnapshotID), errCleanupDeferred)
			w.abandoned = old.SnapshotID
			require.ErrorIs(t, w.deleteUnpublished(t.Context(), store.current), errCleanupDeferred)
			require.NoError(t, w.cleanup(t.Context()))
			require.Contains(t, store.versions, old.SnapshotID)
			require.NotContains(t, store.deletions, old.SnapshotID)
			require.Equal(t, old.SnapshotID, w.abandoned)
		})
	}
}

func TestPublicationDeferredOrphanDoesNotBlockSourceAndIsEventuallyDeleted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		z := newFakeZooKeeper()
		z.online = false
		w := newWorker(testConfig(), store)
		w.publication = newPublication(z, time.Minute)
		store.insertError = context.DeadlineExceeded
		require.ErrorIs(t, w.refreshSnapshot(t.Context()), context.DeadlineExceeded)
		orphan := w.abandoned
		require.Contains(t, store.versions, orphan)
		store.insertError = nil
		reads := store.sourceReads
		require.NoError(t, w.refreshSnapshot(t.Context()))
		require.Equal(t, reads+1, store.sourceReads, "deferred orphan must not suppress source scanning")
		require.Error(t, w.progressZooKeeper(t.Context()))
		w.publication.degrade(errZooKeeperUnavailable)
		require.NoError(t, w.resolve(t.Context()))
		require.Equal(t, orphan, w.abandoned)
		require.ErrorIs(t, w.cleanupPending(t.Context()), errCleanupDeferred)
		z.online = true
		require.NoError(t, w.progressZooKeeper(t.Context()))
		time.Sleep(time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.NoError(t, w.cleanupPending(t.Context()))
		require.NotContains(t, store.versions, orphan)
		require.NoError(t, w.refreshSnapshot(t.Context()))
		require.Empty(t, w.abandoned)
		require.Equal(t, 2, store.inserts)
	})
}

func TestPublicationStructuredPhaseLogs(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		previous := log.Logger
		log.Logger = zerolog.New(&output)
		defer func() { log.Logger = previous }()
		store := newFakeStore(t)
		z := newFakeZooKeeper()
		s := newService(testConfig())
		defer s.cancel()
		s.client, s.store, s.session = &mongo.Client{}, &mongoStore{}, &fakeSession{}
		s.worker = newWorker(s.config, store)
		s.zooKeeper = z
		s.attemptRefresh()
		require.NotContains(t, output.String(), "Subscription snapshot published")
		require.NotContains(t, output.String(), "Subscription snapshot ZooKeeper activated")
		time.Sleep(time.Minute)
		z.beforeRead = func(context.Context, string) error { return zk.ErrNoAuth }
		s.attemptPublication()
		require.False(t, z.nodes[activatedNode].exists)
		s.attemptCleanup()
		z.beforeRead = nil
		s.worker.publication.retryBlocked = false
		s.attemptPublication()
		s.attemptCleanup()
		messages := make(map[string][]map[string]any)
		var ordered []string
		for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
			var entry map[string]any
			require.NoError(t, json.Unmarshal([]byte(line), &entry))
			message, ok := entry["message"].(string)
			require.True(t, ok)
			messages[message] = append(messages[message], entry)
			switch message {
			case "Subscription snapshot ZooKeeper prepared", "Subscription snapshot published", "Subscription snapshot ZooKeeper activated":
				ordered = append(ordered, message)
			}
		}
		require.Equal(t, []string{
			"Subscription snapshot ZooKeeper prepared", "Subscription snapshot published", "Subscription snapshot ZooKeeper activated",
		}, ordered)
		id := store.current.Version.SnapshotID
		for _, message := range ordered {
			require.Len(t, messages[message], 1)
			require.Equal(t, id, messages[message][0]["snapshotId"])
		}
		require.Equal(t, float64(0), messages[ordered[0]][0]["durationMs"])
		require.Equal(t, float64(60000), messages[ordered[1]][0]["durationMs"])
		require.Equal(t, float64(60000), messages[ordered[2]][0]["durationMs"])
		failure := messages["Subscription snapshot ZooKeeper operation failed"][0]
		require.Equal(t, activatedNode, failure["phase"])
		require.Equal(t, "access", failure["errorCategory"])
		require.Equal(t, id, failure["snapshotId"])
		require.Contains(t, failure, "durationMs")
		require.Len(t, messages["Subscription snapshot cleanup deferred"], 1)
		require.Len(t, messages["Subscription snapshot cleanup completed"], 1)
		require.Len(t, messages["Subscription snapshots continuing with MongoDB fallback"], 1)
		require.Len(t, messages["Subscription snapshots returned to synchronized publication"], 1)
	})
}

func TestPublicationStartupWaitsForItsOwnConfirmedMongoHead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		require.NoError(t, newWorker(testConfig(), store).refreshSnapshot(t.Context()))
		a := store.current.Version
		require.NoError(t, newWorker(testConfig(), store).refreshSnapshot(t.Context()))
		b := store.current.Version
		z := newFakeZooKeeper()
		z.nodes[preparedNode] = zNode{exists: true, czxid: 1, value: b}
		z.nodes[activatedNode] = zNode{exists: true, czxid: 2, value: a}
		w := newWorker(testConfig(), store)
		w.publication = newPublication(z, time.Minute)
		w.publication.degraded = true
		require.NoError(t, w.refreshSnapshot(t.Context()))
		c := w.pending.next.Version
		store.activation = func(string, head) (bool, error) { return false, context.DeadlineExceeded }
		require.ErrorIs(t, w.resolve(t.Context()), context.DeadlineExceeded)
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.Empty(t, z.writes, "reconnect must not resume the pre-restart B while startup C is unconfirmed")
		store.activation = nil
		require.NoError(t, w.resolve(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.True(t, sameDescriptor(c, z.nodes[preparedNode].value))
		require.True(t, sameDescriptor(a, z.nodes[activatedNode].value))
		time.Sleep(time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.True(t, sameDescriptor(c, z.nodes[activatedNode].value))
		require.Equal(t, []string{preparedNode, activatedNode}, z.writes)
	})
}

func TestPublicationCatchUpRequiresConfirmedMongoHead(t *testing.T) {
	for _, ownProposal := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown head", true: "own uncertain CAS"}[ownProposal], func(t *testing.T) {
			store := newFakeStore(t)
			w := newWorker(testConfig(), store)
			require.NoError(t, w.refreshSnapshot(t.Context()))
			a := store.current.Version
			z := newFakeZooKeeper()
			z.nodes[preparedNode] = zNode{exists: true, czxid: 1, value: a}
			z.nodes[activatedNode] = zNode{exists: true, czxid: 2, value: a}
			w.publication = newPublication(z, time.Minute)
			w.publication.latest = a
			w.publication.degraded = true
			w.publication.firstActivated = true
			store.source[0] = sourceDocument(t, "a", "new proposal")
			require.NoError(t, w.refreshSnapshot(t.Context()))
			if ownProposal {
				store.current = w.pending.next
			} else {
				unknown := testDescriptor(time.Now(), 1)
				store.versions[unknown.SnapshotID] = unknown.DocumentCount
				store.current = proposeHead(store.current, unknown, w.config.MinimumRetainedSnapshots)
			}
			require.Error(t, w.progressZooKeeper(t.Context()), "a MongoDB read alone does not confirm this worker's CAS")
			require.Empty(t, z.writes)
			if ownProposal {
				require.NoError(t, w.resolve(t.Context()))
				require.NoError(t, w.progressZooKeeper(t.Context()))
				require.True(t, sameDescriptor(store.current.Version, z.nodes[preparedNode].value))
			} else {
				require.Error(t, w.resolve(t.Context()))
				require.Error(t, w.progressZooKeeper(t.Context()))
				require.Empty(t, z.writes, "unknown MongoDB conflicts also block unsafe ZooKeeper releases")
				require.Error(t, w.cleanupPending(t.Context()))
				require.Empty(t, store.deletions)
			}
		})
	}
}

func TestPublicationInsertMustBeCompleteIncludingEmptySnapshot(t *testing.T) {
	for _, scenario := range []string{"empty", "source failure", "insert failure"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeStore(t)
				z := newFakeZooKeeper()
				w := newWorker(testConfig(), store)
				w.publication = newPublication(z, time.Minute)
				switch scenario {
				case "empty":
					store.source = nil
				case "source failure":
					store.sourceError = context.DeadlineExceeded
				case "insert failure":
					store.insertError = context.DeadlineExceeded
				}
				err := w.refreshSnapshot(t.Context())
				if scenario != "empty" {
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.NoError(t, w.progressZooKeeper(t.Context()))
					require.Empty(t, z.writes)
					require.Empty(t, store.current.Version.SnapshotID)
					return
				}
				require.NoError(t, err)
				require.NoError(t, w.progressZooKeeper(t.Context()))
				require.Zero(t, z.nodes[preparedNode].value.DocumentCount)
				require.Empty(t, store.current.Version.SnapshotID)
				time.Sleep(time.Minute)
				require.NoError(t, w.resolve(t.Context()))
				require.NoError(t, w.progressZooKeeper(t.Context()))
				require.True(t, sameDescriptor(store.current.Version, z.nodes[activatedNode].value))
				require.Zero(t, store.current.Version.DocumentCount)
			})
		})
	}
}
