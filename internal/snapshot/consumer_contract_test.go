// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

type simulatedConsumer struct {
	applied  descriptor
	fallback bool
}

func (c *simulatedConsumer) poll(t *testing.T, store *fakeStore, z *fakeZooKeeper) {
	t.Helper()
	current := store.current.Version
	activated := z.nodes[activatedNode]
	target := current
	c.fallback = true
	if z.online && activated.exists && (sameDescriptor(activated.value, c.applied) ||
		sameDescriptor(activated.value, current)) {

		target = activated.value
		c.fallback = false
	}
	require.NoError(t, validateDescriptor(target))
	if store.versions[target.SnapshotID] == target.DocumentCount {
		c.applied = target
	} else {
		c.fallback = true
	}
}

func TestConsumerFallbackAndSafeReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, store, z := publicationService(t, 20*time.Second)
		signalZooKeeper(z, false)
		synctest.Wait()
		consumer := &simulatedConsumer{}
		consumer.poll(t, store, z)
		require.True(t, consumer.fallback, "missing initial activated permits MongoDB fallback")
		b := consumer.applied
		store.source[0] = sourceDocument(t, "a", "F")
		s.advance(20 * time.Second)
		synctest.Wait()
		consumer.poll(t, store, z)
		f := consumer.applied
		require.NotEqual(t, b.SnapshotID, f.SnapshotID)
		signalZooKeeper(z, true)
		synctest.Wait()
		consumer.poll(t, store, z)
		require.True(t, consumer.fallback, "reconnect alone cannot switch the consumer")
		require.True(t, sameDescriptor(f, consumer.applied))
		s.advance(40 * time.Second)
		synctest.Wait()
		consumer.poll(t, store, z)
		require.True(t, consumer.fallback, "lagging B must not replace applied F")
		store.source[0] = sourceDocument(t, "a", "G")
		s.advance(20 * time.Second)
		synctest.Wait()
		consumer.poll(t, store, z)
		g := consumer.applied
		require.NotEqual(t, f.SnapshotID, g.SnapshotID)
		s.advance(40 * time.Second)
		synctest.Wait()
		consumer.poll(t, store, z)
		require.True(t, consumer.fallback, "ZooKeeper F cannot replace MongoDB G")
		require.True(t, sameDescriptor(g, consumer.applied))
		s.advance(time.Minute)
		synctest.Wait()
		consumer.poll(t, store, z)
		require.False(t, consumer.fallback)
		require.True(t, sameDescriptor(g, consumer.applied))
		s.shutdown()
	})
}

func TestConsumerRejectsIncompleteSnapshotAndChangedMetadata(t *testing.T) {
	store := newFakeStore(t)
	a := testDescriptor(time.Now(), 1)
	b := testDescriptor(time.Now(), 2)
	store.current = proposeHead(store.current, b, 3)
	store.versions[a.SnapshotID] = 1
	store.versions[b.SnapshotID] = 1
	z := newFakeZooKeeper()
	z.nodes[activatedNode] = zNode{exists: true, value: b}
	consumer := &simulatedConsumer{applied: a}
	consumer.poll(t, store, z)
	require.True(t, sameDescriptor(a, consumer.applied), "partial snapshot is discarded")
	require.True(t, consumer.fallback, "incomplete data cannot complete the return to ZooKeeper")
	store.versions[b.SnapshotID] = 2
	consumer.poll(t, store, z)
	require.True(t, sameDescriptor(b, consumer.applied), "consumer can load without observing prepare")
	corrupted := b
	corrupted.SourceHash = a.SourceHash[:63] + "f"
	z.nodes[activatedNode] = zNode{exists: true, value: corrupted}
	consumer.poll(t, store, z)
	require.True(t, consumer.fallback, "full descriptor, not only snapshot ID, governs safe return")
	require.True(t, sameDescriptor(b, consumer.applied))
}
