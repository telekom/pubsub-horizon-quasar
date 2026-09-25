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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson"
)

type fakeStore struct {
	current      head
	source       []bson.Raw
	versions     map[string]int64
	inserts      int
	activations  int
	deletions    []string
	readError    error
	sourceError  error
	insertError  error
	activation   func(string, head) (bool, error)
	beforeDelete func()
	sourceReads  int
	cleanups     int
	beforeRead   func(context.Context) error
}

func testConfig() config.SubscriptionSnapshots {
	return config.SubscriptionSnapshots{
		Enabled: true, URI: "mongodb://localhost:27017", Database: "test-horizon-config",
		SourceCollection: "subscriptions", SnapshotCollection: "snapshots", HeadCollection: "heads",
		RefreshInterval: time.Minute, CleanupInterval: time.Hour, RetentionTime: 168 * time.Hour,
		MinimumRetainedSnapshots: 3, OperationTimeout: 10 * time.Second, MaxSnapshotBytes: 64 * 1024 * 1024,
	}
}

func newFakeStore(t *testing.T) *fakeStore {
	return &fakeStore{
		current: head{ID: "head", RecentSnapshots: []descriptor{}},
		source:  []bson.Raw{sourceDocument(t, "a", "initial")}, versions: make(map[string]int64),
	}
}

func (f *fakeStore) readHead(context.Context) (head, error) {
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

func (f *fakeStore) visitSnapshotIDs(_ context.Context, visit func(string) error) error {
	f.cleanups++
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
		name          string
		existing      bool
		restart       bool
		activeCount   int64
		sourceChanged bool
		want          publicationReason
	}{
		{name: "initial", want: reasonInitial},
		{name: "active count lower", existing: true, activeCount: 0, want: reasonSnapshotCountMismatch},
		{name: "active count higher", existing: true, activeCount: 2, want: reasonSnapshotCountMismatch},
		{name: "restart unchanged", existing: true, restart: true, activeCount: 1, want: reasonRestart},
		{
			name: "restart with changed source", existing: true, restart: true, activeCount: 1,
			sourceChanged: true, want: reasonRestart,
		},
		{
			name: "restart with incomplete snapshot", existing: true, restart: true, activeCount: 0,
			sourceChanged: true, want: reasonSnapshotCountMismatch,
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

			require.NoError(t, w.refresh(ctx))
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry), "expected exactly one publish log")
			require.Equal(t, "Subscription snapshot published", entry["message"])
			require.Equal(t, string(tt.want), entry["snapshotReason"])
			require.Equal(t, store.current.Version.SnapshotID, entry["snapshotId"])
			require.Equal(t, float64(store.current.Version.DocumentCount), entry["documentCount"])
			require.Contains(t, entry, "durationMs")
			require.NotContains(t, entry, "startup")
			require.NotContains(t, entry, "sourceHashChanged")
			require.NotContains(t, entry, "activeSnapshotComplete")

			output.Reset()
			require.NoError(t, w.refresh(ctx))
			require.Empty(t, output.String(), "unchanged refresh must not log a publication")
		})
	}
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
			require.NoError(t, w.refresh(context.Background()))
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
			require.Nil(t, w.pending)
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
			require.NotNil(t, w.pending)
			require.Error(t, w.cleanup(ctx, time.Now().Add(30*24*time.Hour)))
			require.Empty(t, store.deletions)
			store.source[0] = sourceDocument(t, "a", "must not rebuild pending candidate")
			store.activation = nil
			var output bytes.Buffer
			previousLogger := log.Logger
			log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
			t.Cleanup(func() { log.Logger = previousLogger })
			require.NoError(t, w.refresh(ctx))
			var entry map[string]any
			require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
			require.Equal(t, string(reasonInitial), entry["snapshotReason"])
			require.True(t, sameHead(candidate, store.current))
			require.Equal(t, 1, store.inserts)
			require.Len(t, store.current.RecentSnapshots, 1)
			require.Nil(t, w.pending)
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
	require.Error(t, restarted.cleanup(ctx, time.Now().Add(30*24*time.Hour)))
	store.activation = nil
	var output bytes.Buffer
	previousLogger := log.Logger
	log.Logger = zerolog.New(&output).Level(zerolog.InfoLevel)
	t.Cleanup(func() { log.Logger = previousLogger })
	require.NoError(t, restarted.refresh(ctx))
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, string(reasonInitial), entry["snapshotReason"])
	require.Equal(t, candidateID, store.current.Version.SnapshotID)
	require.Equal(t, delayed.next.Version, store.current.RecentSnapshots[1])
	require.False(t, store.apply(delayed.previous.Version.SnapshotID, delayed.next), "delayed CAS cannot match again")
	require.Equal(t, 2, store.inserts)
}

func TestRetentionProtectsExactPredecessors(t *testing.T) {
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
		require.NoError(t, w.cleanup(context.Background(), now))
		require.Contains(t, store.versions, boundary.SnapshotID, "exact cutoff must not expire")
		require.NotContains(t, store.versions, expired.SnapshotID)
		for _, version := range store.current.RecentSnapshots {
			require.NotContains(t, store.deletions, version.SnapshotID)
		}
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
			require.Error(t, w.cleanup(context.Background(), time.Now()))
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
	require.ErrorContains(t, w.cleanup(ctx, time.Now().Add(30*24*time.Hour)), "reset to bootstrap")
	require.Equal(t, 1, store.inserts)
	require.Empty(t, store.deletions)
}

func TestDisabledServiceHasNoSideEffects(t *testing.T) {
	var output bytes.Buffer
	previousLogger := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = previousLogger })

	require.NoError(t, Start(config.SubscriptionSnapshots{}))
	require.JSONEq(t, `{"level":"info","enabled":false,"message":"Subscription snapshots disabled"}`, output.String())
	output.Reset()
	disabled := testConfig()
	disabled.Enabled = false
	disabled.URI = "mongodb+srv://example.invalid:27017/?journal=false"
	require.NoError(t, Start(disabled), "disabled snapshots must not parse or connect to MongoDB")
	require.JSONEq(t, `{"level":"info","enabled":false,"message":"Subscription snapshots disabled"}`, output.String())
	output.Reset()
	invalid := testConfig()
	invalid.URI = ""
	require.Error(t, Start(invalid))
	require.Empty(t, output.String(), "invalid configuration must not announce startup")
}

func TestStartupLogging(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			var output bytes.Buffer
			previousLogger := log.Logger
			log.Logger = zerolog.New(&output)
			t.Cleanup(func() { log.Logger = previousLogger })

			c := testConfig()
			c.Enabled = enabled
			c.InitialStartDelay = time.Minute
			c.URI = "mongodb://snapshot-user:test-password@localhost:27017/?authSource=private-auth"
			c.RefreshInterval = 37 * time.Second
			c.CleanupInterval = 2 * time.Hour
			c.RetentionTime = 240 * time.Hour
			c.MinimumRetainedSnapshots = 4
			c.MaxSnapshotBytes = 33554432
			c.OperationTimeout = 7 * time.Second
			logStartup(c)

			expected := `{"level":"info","enabled":false,"message":"Subscription snapshots disabled"}`
			if enabled {
				expected = `{
					"level":"info",
					"enabled":true,
					"database":"test-horizon-config",
					"sourceCollection":"subscriptions",
					"snapshotCollection":"snapshots",
					"headCollection":"heads",
					"initialStartDelay":"1m0s",
					"refreshInterval":"37s",
					"cleanupInterval":"2h0m0s",
					"retentionTime":"240h0m0s",
					"minimumRetainedSnapshots":4,
					"maxSnapshotBytes":33554432,
					"operationTimeout":"7s",
					"message":"Starting subscription snapshot worker"
				}`
			}
			require.JSONEq(t, expected, output.String())
			require.Equal(t, 1, strings.Count(output.String(), "\n"), "startup must emit exactly one event")
			for _, sensitive := range []string{c.URI, "snapshot-user", "test-password", "private-auth"} {
				require.NotContains(t, output.String(), sensitive)
			}
		})
	}
}

func TestServiceSchedulingAndCancellation(t *testing.T) {
	s := newService(testConfig())
	// A canceled context must stop a service even when no ticker ever fires.
	s.cancel()
	refresh := make(chan time.Time)
	go func() {
		defer close(s.done)
		defer s.disconnect()
		s.loop(refresh)
	}()
	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop on cancellation")
	}
	s.shutdown()
	s.shutdown()
}
