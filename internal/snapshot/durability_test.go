// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
)

func configureTestFailpoint(t *testing.T, client *mongo.Client, name string, mode any, data bson.D) (int64, func()) {
	t.Helper()
	var result struct {
		Count int64 `bson:"count"`
	}
	command := bson.D{{Key: "configureFailPoint", Value: name}, {Key: "mode", Value: mode}}
	if data != nil {
		command = append(command, bson.E{Key: "data", Value: data})
	}
	require.NoError(t, client.Database("admin").RunCommand(t.Context(), command).Decode(&result))
	var once sync.Once
	disable := func() {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			require.NoError(t, client.Database("admin").RunCommand(ctx, bson.D{
				{Key: "configureFailPoint", Value: name}, {Key: "mode", Value: "off"},
			}).Err())
		})
	}
	t.Cleanup(disable)
	return result.Count, disable
}

func waitForTestFailpoint(t *testing.T, client *mongo.Client, name string, previousCount int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.NoError(t, client.Database("admin").RunCommand(ctx, bson.D{
		{Key: "waitForFailPoint", Value: name},
		{Key: "timesEntered", Value: previousCount + 1},
		{Key: "maxTimeMS", Value: 8000},
	}).Err())
}

func pauseSecondaryReplication(t *testing.T, client *mongo.Client) func() {
	t.Helper()
	var hello struct {
		Hosts   []string `bson:"hosts"`
		Primary string   `bson:"primary"`
	}
	require.NoError(t, client.Database("admin").RunCommand(t.Context(), bson.D{{Key: "hello", Value: 1}}).Decode(&hello))
	var releases []func()
	for _, host := range hello.Hosts {
		if host == hello.Primary {
			continue
		}
		direct, err := mongo.Connect(t.Context(), options.Client().
			ApplyURI("mongodb://"+host+"/?directConnection=true").SetServerSelectionTimeout(5*time.Second))
		require.NoError(t, err)
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			require.NoError(t, direct.Disconnect(ctx))
		})
		count, release := configureTestFailpoint(t, direct, "rsSyncApplyStop", "alwaysOn", nil)
		waitForTestFailpoint(t, direct, "rsSyncApplyStop", count)
		releases = append(releases, release)
	}
	require.Len(t, releases, 2, "both secondaries must pause to hold back majority visibility")
	return func() {
		for _, release := range releases {
			release()
		}
	}
}

func testMongoDelayedVisibility(t *testing.T, uri string) {
	client, store, w := mongoFixture(t, uri)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	s := &service{client: client, store: store}
	require.NoError(t, s.withSession(ctx, w.refresh))
	t.Cleanup(func() { s.session.EndSession(context.Background()) })
	before, err := store.readHead(ctx)
	require.NoError(t, err)
	_, err = store.source.ReplaceOne(ctx, bson.D{{Key: "_id", Value: "a"}}, sourceDocument(t, "a", "new source"))
	require.NoError(t, err)
	release := pauseSecondaryReplication(t, client)

	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result <- s.withSession(ctx, w.refresh)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(10 * time.Second):
			t.Error("blocked publication did not stop")
		}
	})
	local, err := store.snapshots.Clone(options.Collection().SetReadConcern(readconcern.Local()))
	require.NoError(t, err)
	var candidate struct {
		ID string `bson:"snapshotId"`
	}
	require.Eventually(t, func() bool {
		err := local.FindOne(ctx, bson.D{{Key: "snapshotId", Value: bson.D{
			{Key: "$ne", Value: before.Version.SnapshotID},
		}}}).Decode(&candidate)
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	localCount, err := local.CountDocuments(ctx, bson.D{{Key: "snapshotId", Value: candidate.ID}})
	require.NoError(t, err)
	require.Equal(t, int64(3), localCount, "inserts have reached the primary")
	majorityCount, err := store.countSnapshot(ctx, candidate.ID)
	require.NoError(t, err)
	require.Zero(t, majorityCount, "uncommitted candidate must remain invisible to majority readers")
	unchanged, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(before, unchanged), "head must not advance before majority insert acknowledgement")
	select {
	case err := <-result:
		t.Fatalf("publication finished before replication resumed: %v", err)
	default:
	}
	release()
	select {
	case err := <-result:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("publication failed to finish after replication resumed")
	}
	require.NoError(t, s.withSession(ctx, func(sessionCtx context.Context) error {
		active, err := store.readHead(sessionCtx)
		if err != nil {
			return err
		}
		require.Equal(t, candidate.ID, active.Version.SnapshotID)
		complete, err := readCompleteSnapshot(sessionCtx, store, active, func() {})
		require.True(t, complete, "causal majority reader must observe a complete activated snapshot")
		return err
	}))
}

func testMongoInflightShutdown(t *testing.T, uri string) {
	existingClient, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	c := w.config
	c.OperationTimeout = 2 * time.Minute
	s := newService(c)
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetAppName("snapshot-shutdown-test"))
	require.NoError(t, err)
	s.client = client
	require.NoError(t, s.initialize(ctx))
	require.NoError(t, s.withSession(ctx, s.worker.refresh))
	before, err := store.readHead(ctx)
	require.NoError(t, err)
	count, _ := configureTestFailpoint(t, existingClient, "failCommand", bson.D{{Key: "times", Value: 1}}, bson.D{
		{Key: "failCommands", Value: bson.A{"find"}},
		{Key: "appName", Value: "snapshot-shutdown-test"},
		{Key: "blockConnection", Value: true},
		{Key: "blockTimeMS", Value: 15000},
	})
	t.Cleanup(s.shutdown)
	go s.run()
	waitForTestFailpoint(t, existingClient, "failCommand", count)
	start := time.Now()
	s.shutdown()
	require.Less(t, time.Since(start), shutdownTimeout)
	select {
	case <-s.done:
	default:
		t.Fatal("shutdown returned without completing client disconnection")
	}
	require.ErrorIs(t, client.Ping(ctx, nil), mongo.ErrClientDisconnected)
	require.NoError(t, existingClient.Ping(ctx, nil), "unrelated connection must remain usable")
	after, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(before, after))
}
