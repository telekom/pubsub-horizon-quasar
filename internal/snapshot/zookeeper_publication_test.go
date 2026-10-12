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

// TestPublicationNormalTimerAndOrder checks preparation, the exact delay boundary and MongoDB-before-ZooKeeper activation.
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
		require.Nil(t, s.worker.proposal)
		require.Nil(t, s.worker.publication.candidate)
		s.shutdown()
	})
}

// TestPublicationDegradedLatestStateAndFixedCatchUp checks recovery to the latest fallback head without new MongoDB writes.
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

// TestPublicationDisconnectDuringWaitingAndNewHead checks that fixed catch-up candidates never roll MongoDB back.
func TestPublicationDisconnectDuringWaitingAndNewHead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 20*time.Second)
		b := z.nodes["prepared"].value
		preparedAt := s.worker.publication.candidate.preparedAt
		headReads := 0
		store.beforeHead = func(context.Context) error {
			headReads++
			return nil
		}
		signalZooKeeper(z, false)
		synctest.Wait()
		require.True(t, sameDescriptor(b, store.current.Version), "fallback must not wait for activation delay")
		require.Equal(t, 1, headReads, "offline wake must read the head only for MongoDB publication, not cleanup")
		require.True(t, s.worker.cleanupDue, "offline wake must leave cleanup pending")
		store.beforeHead = nil
		store.source[0] = sourceDocument(t, "a", "newer C")
		s.advance(20 * time.Second)
		synctest.Wait()
		c := store.current.Version
		require.NotEqual(t, b.SnapshotID, c.SnapshotID)
		require.Equal(t, preparedAt, s.worker.publication.candidate.preparedAt)
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
		require.True(t, sameDescriptor(c, s.worker.publication.candidate.value))
		s.advance(40 * time.Second)
		synctest.Wait()
		require.True(t, sameDescriptor(c, z.nodes["activated"].value))
		require.True(t, sameDescriptor(g, z.nodes["prepared"].value))
		require.True(t, sameDescriptor(g, store.current.Version))
		s.shutdown()
	})
}

// TestPublicationOfflineWakesDeferCleanupUntilRefresh checks that repeated offline events do not trigger cleanup attempts.
func TestPublicationOfflineWakesDeferCleanupUntilRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 300*time.Second)
		s.advance(s.config.ActivationDelay)
		synctest.Wait()
		cleanups, sourceReads := store.cleanups, store.sourceReads
		orphan := testDescriptor(time.Now().Add(-time.Hour), 1)
		store.versions[orphan.SnapshotID] = 1
		s.worker.cleanupDue = true
		headReads := 0
		store.beforeHead = func(context.Context) error {
			headReads++
			return nil
		}
		var output bytes.Buffer
		previous := log.Logger
		log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
		t.Cleanup(func() { log.Logger = previous })
		for range 10 {
			signalZooKeeper(z, false)
			synctest.Wait()
		}
		require.Zero(t, headReads, "offline SDK chatter must not trigger cleanup head reads")
		require.NotContains(t, output.String(), "Subscription snapshot cleanup deferred")
		require.True(t, s.worker.cleanupDue)
		require.Equal(t, cleanups, store.cleanups)
		require.Contains(t, store.versions, orphan.SnapshotID)

		s.advance(s.config.RefreshInterval - s.config.ActivationDelay - time.Nanosecond)
		synctest.Wait()
		require.Zero(t, headReads, "cleanup must remain deferred until the refresh tick")
		s.advance(time.Nanosecond)
		synctest.Wait()
		require.Equal(t, 2, headReads, "refresh and one cleanup retry each read the head")
		require.Equal(t, sourceReads+1, store.sourceReads)
		require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("Subscription snapshot cleanup deferred")))
		require.True(t, s.worker.cleanupDue)
		require.Equal(t, cleanups, store.cleanups)
		require.Contains(t, store.versions, orphan.SnapshotID)

		for range 10 {
			signalZooKeeper(z, false)
			synctest.Wait()
		}
		require.Equal(t, 2, headReads, "later offline events must not repeat the tick's cleanup attempt")
		require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("Subscription snapshot cleanup deferred")))
		s.shutdown()
	})
}

// TestPublicationRecoveryAfterUsedWakeResumesCleanup checks renewed recovery work after a disconnect without repeated retries.
func TestPublicationRecoveryAfterUsedWakeResumesCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 300*time.Second)
		s.advance(s.config.ActivationDelay)
		synctest.Wait()
		signalZooKeeper(z, true)
		synctest.Wait()
		cleanups, sourceReads := store.cleanups, store.sourceReads
		inserts, activations := store.inserts, store.activations
		orphan := testDescriptor(time.Now().Add(-time.Hour), 1)
		store.versions[orphan.SnapshotID] = 1
		s.worker.cleanupDue = true
		signalZooKeeper(z, false)
		synctest.Wait()
		require.True(t, s.worker.cleanupDue)
		require.True(t, s.worker.publication.degraded)
		require.Equal(t, cleanups, store.cleanups)
		require.Contains(t, store.versions, orphan.SnapshotID)

		z.session++
		signalZooKeeper(z, true)
		synctest.Wait()
		require.False(t, s.worker.cleanupDue, "recovery must not wait for the refresh tick after an earlier online wake")
		require.False(t, s.worker.publication.degraded)
		require.Equal(t, cleanups+1, store.cleanups)
		require.NotContains(t, store.versions, orphan.SnapshotID)
		require.Equal(t, sourceReads, store.sourceReads)
		require.Equal(t, inserts, store.inserts)
		require.Equal(t, activations, store.activations)
		require.Len(t, z.writes, 2, "recovery of synchronized nodes must not rewrite them")

		s.worker.cleanupDue = true
		headReads := 0
		store.beforeHead = func(context.Context) error {
			headReads++
			return nil
		}
		for range 10 {
			signalZooKeeper(z, true)
			synctest.Wait()
		}
		require.Zero(t, headReads, "repeated online events must still respect the consumed wake budget")
		require.Equal(t, cleanups+1, store.cleanups)
		require.True(t, s.worker.cleanupDue)
		s.shutdown()
	})
}

// TestPublicationUncertainWritesReadBackAndOriginalCAS checks queued and committed writes across session changes.
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

// testUncertainZooKeeperWrite simulates a lost reply and verifies recovery with the original node version expectation.
func testUncertainZooKeeperWrite(t *testing.T, name string, applied bool) {
	t.Helper()
	store := newFakeStore(t)
	z := newFakeZooKeeper()
	w := newWorker(testConfig(), store)
	w.publication = newZooKeeperPublication(z, time.Minute)
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
	candidate := w.publication.candidate.value
	if w.proposal != nil {
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
		require.Equal(t, time.Now(), w.publication.candidate.preparedAt)
		time.Sleep(time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
	}
	require.True(t, sameDescriptor(candidate, z.nodes["activated"].value))
	require.Equal(t, success, w.lastSuccess, "ZooKeeper success must not update MongoDB success time")
	require.Equal(t, []string{"prepared", "activated"}, z.writes)
}

// TestPublicationCleanupAndIntegrityRecovery checks protected old references and recovery after restoring known node identity.
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

// TestPublicationPrepareTimeoutUsesFreshMongoBudget checks that ZooKeeper timeout leaves a fresh budget for fallback activation.
func TestPublicationPrepareTimeoutUsesFreshMongoBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		z := newFakeZooKeeper()
		c := testConfig()
		c.RefreshTimeout = 30 * time.Second
		s := newService(c)
		s.client, s.store, s.session = &mongo.Client{}, &mongoStore{}, &fakeSession{}
		s.worker = newWorker(c, store)
		s.worker.publication = newZooKeeperPublication(z, time.Minute)
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
		require.NotNil(t, s.worker.publication.candidate)
		require.Nil(t, s.worker.proposal)
		s.cancel()
	})
}

// TestPublicationShutdownAndRetryScheduling checks tick-bounded retries and cancellation of pending activation.
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

// TestPublicationStartupAbandonsOldPrepare checks that restart prepares its own snapshot rather than releasing old work.
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
		w.publication = newZooKeeperPublication(z, time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.Empty(t, z.writes, "startup must not release the old MongoDB head")
		require.NoError(t, w.refreshSnapshot(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		c := w.proposal.next.Version
		require.NotEqual(t, b.SnapshotID, c.SnapshotID)
		require.True(t, sameDescriptor(c, z.nodes["prepared"].value))
		require.True(t, sameDescriptor(a, z.nodes["activated"].value))
		time.Sleep(time.Minute)
		require.NoError(t, w.resolve(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.True(t, sameDescriptor(c, z.nodes["activated"].value))
	})
}

// TestWriteOutcomeClassification checks uncertain transport failures against definite server-side write rejections.
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

// TestPublicationDelayedPreviousProcessWrites checks that stale version guards reject late writes from an earlier process.
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
		w.publication = newZooKeeperPublication(z, time.Minute)
		require.NoError(t, w.refreshSnapshot(t.Context()))
		require.NoError(t, w.progressZooKeeper(t.Context()))
		c := w.proposal.next.Version
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

// TestPublicationRejectedRetryKeepsUncertainOriginalWrite checks that a rejected retry cannot discard an older uncertain write.
func TestPublicationRejectedRetryKeepsUncertainOriginalWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		z := newFakeZooKeeper()
		w := newWorker(testConfig(), store)
		w.publication = newZooKeeperPublication(z, time.Minute)
		require.NoError(t, w.refreshSnapshot(t.Context()))
		z.beforeWrite = func(context.Context, string, descriptor, zNode) (writeOutcome, error) {
			return writeUncertain, context.DeadlineExceeded
		}
		require.ErrorIs(t, w.progressZooKeeper(t.Context()), context.DeadlineExceeded)
		original := *w.publication.candidate.operation
		b := w.publication.candidate.value
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
		require.True(t, w.publication.candidate.operation.uncertain)
		require.True(t, sameDescriptor(b, w.publication.candidate.value))
		require.True(t, sameNode(original.expected, w.publication.candidate.operation.expected))
		require.ErrorIs(t, w.cleanupPending(t.Context()), errCleanupDeferred)
		require.Empty(t, store.deletions)
		// Rejecting a later retry does not prove the original queued write was cancelled.
		_, err := z.apply(preparedNode, b, original.expected)
		require.NoError(t, err, "original request can still execute after retry rejection")
		z.session++
		z.beforeWrite = nil
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.Equal(t, time.Now(), w.publication.candidate.preparedAt)
		time.Sleep(time.Minute)
		require.NoError(t, w.progressZooKeeper(t.Context()))
		require.True(t, sameDescriptor(b, z.nodes[activatedNode].value))
		require.True(t, sameDescriptor(latest, z.nodes[preparedNode].value))
		require.True(t, sameDescriptor(latest, store.current.Version))
	})
}

// TestPublicationAccessRecoveryWithoutWake checks that refresh ticks resume catch-up after access permissions are restored.
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

// TestPublicationMongoFailureAndFlappingAreRateLimited checks that session chatter cannot release a failed MongoDB retry gate.
func TestPublicationMongoFailureAndFlappingAreRateLimited(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 5*time.Minute)
		preparedAt := s.worker.publication.candidate.preparedAt
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
		require.Equal(t, preparedAt, s.worker.publication.candidate.preparedAt)
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

// TestPublicationPendingProtectionReportsReason checks contextual deferrals before cleanup references can be read.
func TestPublicationPendingProtectionReportsReason(t *testing.T) {
	tests := []struct {
		name           string
		firstActivated bool
		candidate      bool
		proposal       bool
	}{
		{"initial activation", false, false, false},
		{"ZooKeeper candidate", true, true, false},
		{"MongoDB proposal", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newZooKeeperPublication(newFakeZooKeeper(), time.Minute)
			p.firstActivated = tt.firstActivated
			if tt.candidate {
				p.candidate = &zCandidate{}
			}
			state, err := p.protection(t.Context(), newFakeStore(t), tt.proposal)
			require.ErrorIs(t, err, errCleanupDeferred)
			require.ErrorContains(t, err, "ZooKeeper activation is unconfirmed or publication is still pending")
			require.Equal(t, zState{}, state)
			require.Nil(t, p.observed, "pending publication must not read cleanup references")
		})
	}
}

// TestPublicationAllDeletionPathsProtectExternalHistoryReferences checks every cleanup path against old ZooKeeper references.
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
			w.publication = newZooKeeperPublication(z, time.Minute)
			w.publication.firstActivated = true
			w.publication.latestConfirmedHead = store.current.Version
			deleted, err := w.deleteVersion(t.Context(), store.current, old.SnapshotID)
			require.ErrorIs(t, err, errCleanupDeferred)
			require.ErrorContains(t, err, "snapshot is protected by ZooKeeper references or the latest catch-up target")
			require.Zero(t, deleted)
			w.abandoned = old.SnapshotID
			require.ErrorIs(t, w.deleteUnpublished(t.Context(), store.current), errCleanupDeferred)
			require.NoError(t, w.cleanup(t.Context()))
			require.Contains(t, store.versions, old.SnapshotID)
			require.NotContains(t, store.deletions, old.SnapshotID)
			require.Equal(t, old.SnapshotID, w.abandoned)
		})
	}
}

// TestPublicationDeferredOrphanDoesNotBlockSourceAndIsEventuallyDeleted checks continued scans and safe later orphan cleanup.
func TestPublicationDeferredOrphanDoesNotBlockSourceAndIsEventuallyDeleted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeStore(t)
		z := newFakeZooKeeper()
		z.online = false
		w := newWorker(testConfig(), store)
		w.publication = newZooKeeperPublication(z, time.Minute)
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

// TestPublicationStructuredPhaseLogs checks ordered stage logs, durations and explicit fallback and recovery events.
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
			case "Subscription snapshot created", "Subscription snapshot ZooKeeper prepared",
				"Subscription snapshot published", "Subscription snapshot ZooKeeper activated":
				ordered = append(ordered, message)
			}
		}
		require.Equal(t, []string{
			"Subscription snapshot created", "Subscription snapshot ZooKeeper prepared",
			"Subscription snapshot published", "Subscription snapshot ZooKeeper activated",
		}, ordered)
		id := store.current.Version.SnapshotID
		for _, message := range ordered {
			require.Len(t, messages[message], 1)
			require.Equal(t, "info", messages[message][0]["level"])
			require.Equal(t, id, messages[message][0]["snapshotId"])
		}
		require.Equal(t, float64(0), messages[ordered[0]][0]["durationMs"])
		require.Equal(t, float64(0), messages[ordered[1]][0]["durationMs"])
		require.Equal(t, float64(0), messages[ordered[2]][0]["durationMs"])
		require.Equal(t, float64(0), messages[ordered[3]][0]["durationMs"])
		failure := messages["Subscription snapshot ZooKeeper operation failed"][0]
		require.Equal(t, activatedNode, failure["phase"])
		require.Equal(t, "access", failure["errorCategory"])
		require.Equal(t, id, failure["snapshotId"])
		require.Contains(t, failure, "durationMs")
		require.Len(t, messages["Subscription snapshot cleanup deferred"], 1)
		require.Len(t, messages["Subscription snapshot cleanup completed"], 1)
		require.Equal(t, "debug", messages["Subscription snapshot cleanup completed"][0]["level"])
		require.Equal(t, float64(0), messages["Subscription snapshot cleanup completed"][0]["deletedDocuments"])
		require.NotContains(t, messages, "Subscription snapshot refresh completed")
		require.NotContains(t, messages, "Subscription snapshot refresh attempt finished")
		require.Len(t, messages["Subscription snapshots continuing with MongoDB fallback"], 1)
		require.Len(t, messages["Subscription snapshots returned to synchronized publication"], 1)
	})
}

// TestPublicationStartupWaitsForItsOwnConfirmedMongoHead checks that startup recovery never publishes an unconfirmed head.
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
		w.publication = newZooKeeperPublication(z, time.Minute)
		w.publication.degraded = true
		require.NoError(t, w.refreshSnapshot(t.Context()))
		c := w.proposal.next.Version
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

// TestPublicationCatchUpRequiresConfirmedMongoHead checks that only resolved worker proposals can become catch-up targets.
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
			w.publication = newZooKeeperPublication(z, time.Minute)
			w.publication.latestConfirmedHead = a
			w.publication.degraded = true
			w.publication.firstActivated = true
			store.source[0] = sourceDocument(t, "a", "new proposal")
			require.NoError(t, w.refreshSnapshot(t.Context()))
			if ownProposal {
				store.current = w.proposal.next
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

// TestPublicationInsertMustBeCompleteIncludingEmptySnapshot checks valid empty publication and no release after build failures.
func TestPublicationInsertMustBeCompleteIncludingEmptySnapshot(t *testing.T) {
	for _, scenario := range []string{"empty", "source failure", "insert failure"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeStore(t)
				z := newFakeZooKeeper()
				w := newWorker(testConfig(), store)
				w.publication = newZooKeeperPublication(z, time.Minute)
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
