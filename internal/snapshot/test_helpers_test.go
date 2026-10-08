// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Shopify/zk"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func serviceLogMessages(t *testing.T, output []byte) []map[string]any {
	t.Helper()
	require.NotContains(t, string(output), "Subscription snapshot refresh completed")
	require.NotContains(t, string(output), "Subscription snapshot refresh attempt finished")
	var entries []map[string]any
	if len(bytes.TrimSpace(output)) == 0 {
		return entries
	}
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte("\n")) {
		var entry map[string]any
		require.NoError(t, json.Unmarshal(line, &entry))
		entries = append(entries, entry)
	}
	return entries
}

func marshal(t *testing.T, value any) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(value)
	require.NoError(t, err)
	return raw
}

func sourceDocument(t *testing.T, id, value string) bson.Raw {
	t.Helper()
	return marshal(t, bson.D{
		{Key: "_id", Value: id}, {Key: "spec", Value: bson.D{{Key: "value", Value: value}}},
	})
}

func testDescriptor(at time.Time, count int64) descriptor {
	id := primitive.NewObjectIDFromTimestamp(at)
	return descriptor{
		SnapshotID: id.Hex(), SourceHash: strings.Repeat("a", 64), DocumentCount: count, CreatedAt: id.Timestamp(),
	}
}

func mongoFixture(t *testing.T, uri string) (*mongo.Client, *mongoStore, *worker) {
	t.Helper()
	c := testConfig()
	c.URI = uri
	c.Database = "snapshots_" + primitive.NewObjectID().Hex()
	c.RefreshTimeout = 20 * time.Second
	client, err := mongo.Connect(t.Context(), options.Client().ApplyURI(uri).SetServerSelectionTimeout(20*time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		require.NoError(t, client.Database(c.Database).Drop(ctx))
		require.NoError(t, client.Disconnect(ctx))
	})
	store := newMongoStore(client, c)
	_, err = store.source.InsertMany(t.Context(), []any{
		sourceDocument(t, "c", "third"), sourceDocument(t, "a", "first"), sourceDocument(t, "b", "second"),
	})
	require.NoError(t, err)
	require.NoError(t, store.setup(t.Context()))
	return client, store, newWorker(c, store)
}

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

func (f *fakeZooKeeper) available() bool { return f.online }

func (f *fakeZooKeeper) events() <-chan struct{} { return f.wake }

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
	return configuredPublicationService(t, c, store, transport), store, transport
}

func configuredPublicationService(
	t *testing.T, c config.SubscriptionSnapshots, store *fakeStore, transport *fakeZooKeeper,
) *scheduledService {
	t.Helper()
	s := &scheduledService{service: newService(c), actions: make(chan func(), 1)}
	s.client, s.store, s.session = &mongo.Client{}, &mongoStore{}, &fakeSession{}
	s.worker = newWorker(c, store)
	s.zooKeeper = transport
	s.worker.publication = newZooKeeperPublication(transport, c.ActivationDelay)
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

func signalZooKeeper(f *fakeZooKeeper, online bool) {
	f.online = online
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

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

type fakeStore struct {
	current       head
	source        []bson.Raw
	versions      map[string]int64
	inserts       int
	activations   int
	deletions     []string
	readError     error
	sourceError   error
	insertError   error
	activation    func(string, head) (bool, error)
	beforeDelete  func()
	sourceReads   int
	cleanups      int
	beforeHead    func(context.Context) error
	beforeRead    func(context.Context) error
	beforeCleanup func(context.Context) error
}

func testConfig() config.SubscriptionSnapshots {
	return config.SubscriptionSnapshots{
		Enabled: true, URI: "mongodb://localhost:27017", Database: "test-horizon-config",
		SourceCollection: "subscriptions", SnapshotCollection: "snapshots", HeadCollection: "heads",
		RefreshInterval:          time.Minute,
		MinimumRetainedSnapshots: 3, RefreshTimeout: 10 * time.Second, CleanupTimeout: 2 * time.Minute,
		MaxSnapshotBytes: 64 * 1024 * 1024,
		ActivationDelay:  time.Minute,
		ZooKeeper: config.SnapshotZooKeeper{
			Addresses: []string{"localhost:2181"}, BasePath: "/horizon/subscriptions", SessionTimeout: 10 * time.Second,
		},
	}
}

func newFakeStore(t *testing.T) *fakeStore {
	return &fakeStore{
		current: head{ID: "head", RecentSnapshots: []descriptor{}},
		source:  []bson.Raw{sourceDocument(t, "a", "initial")}, versions: make(map[string]int64),
	}
}

func (w *worker) refresh(ctx context.Context) error {
	if err := w.refreshSnapshot(ctx); err != nil {
		return err
	}
	return w.cleanupPending(ctx)
}

func (w *worker) refreshSnapshot(ctx context.Context) error {
	if w.proposal == nil {
		if err := w.createSnapshot(ctx); err != nil {
			return err
		}
	}
	if w.publication == nil && w.proposal != nil {
		return w.resolve(ctx)
	}
	return nil
}

func (s *service) attemptPublication() {
	s.runCycle(publicationWork)
}

func (f *fakeStore) readHead(ctx context.Context) (head, error) {
	if f.beforeHead != nil {
		if err := f.beforeHead(ctx); err != nil {
			return head{}, err
		}
	}
	return f.current, f.readError
}

func (f *fakeStore) readSource(ctx context.Context, limit int64) (*sourceBuffer, error) {
	f.sourceReads++
	if f.beforeRead != nil {
		if err := f.beforeRead(ctx); err != nil {
			return nil, err
		}
	}
	if f.sourceError != nil {
		return nil, f.sourceError
	}
	buffer := newSourceBuffer()
	for _, document := range f.source {
		if err := buffer.add(document, limit); err != nil {
			return nil, err
		}
	}
	return buffer, nil
}

func (f *fakeStore) insertSnapshot(_ context.Context, id string, source *sourceBuffer) error {
	f.inserts++
	f.versions[id] = int64(len(source.documents))
	return f.insertError
}

func (f *fakeStore) activate(_ context.Context, expected string, next head) (bool, error) {
	f.activations++
	if f.activation != nil {
		return f.activation(expected, next)
	}
	return f.apply(expected, next), nil
}

func (f *fakeStore) apply(expected string, next head) bool {
	if f.current.Version.SnapshotID != expected {
		return false
	}
	f.current = next
	return true
}

func (f *fakeStore) countSnapshot(_ context.Context, id string) (int64, error) {
	return f.versions[id], nil
}

func (f *fakeStore) visitSnapshotIDs(ctx context.Context, visit func(string) error) error {
	f.cleanups++
	if f.beforeCleanup != nil {
		if err := f.beforeCleanup(ctx); err != nil {
			return err
		}
	}
	var ids []string
	for id := range f.versions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeStore) deleteBatch(_ context.Context, id string) (int64, error) {
	if f.beforeDelete != nil {
		f.beforeDelete()
	}
	count := f.versions[id]
	delete(f.versions, id)
	f.deletions = append(f.deletions, id)
	return count, nil
}

func testZooKeeperClient(t *testing.T, addresses []string) *zooKeeperClient {
	t.Helper()
	client := newZooKeeperClient(t.Context(), config.SnapshotZooKeeper{
		Addresses: addresses, BasePath: "/quasar-test/" + primitive.NewObjectID().Hex(),
		SessionTimeout: 3 * time.Second,
	}, 100*time.Millisecond)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		require.NoError(t, client.close(ctx))
	})
	require.Eventually(t, client.available, 30*time.Second, 10*time.Millisecond)
	return client
}
