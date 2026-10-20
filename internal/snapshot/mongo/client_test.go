// Copyright 2024-2026 Deutsche Telekom AG
// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package mongo

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/test"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

const streamTimeout = 10 * time.Second

var standaloneMongoUri string

// TestMain sets up the standalone MongoDB container shared by all non-replicaset tests in this package.
func TestMain(m *testing.M) {
	test.SetupDocker(&test.Options{
		MongoDb: true,
	})

	mongoHost := test.EnvOrDefault("MONGO_HOST", "localhost")
	mongoPort := test.EnvOrDefault("MONGO_PORT", "27017")
	standaloneMongoUri = "mongodb://" + net.JoinHostPort(mongoHost, mongoPort)

	code := m.Run()

	test.TeardownDocker()
	os.Exit(code)
}

func TestClient_NewClient(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		client, err := NewClient(standaloneMongoUri, "quasar_client_test", "source", "target")
		require.NoError(t, err)
		require.NotNil(t, client)

		assert.Equal(t, "source", client.sourceCollection.Name())
		assert.Equal(t, "target", client.targetCollection.Name())
	})

	t.Run("ConnectionFailure", func(t *testing.T) {
		client, err := NewClient("mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=1000", "quasar_client_test", "source", "target")
		assert.Error(t, err)
		assert.Nil(t, client)
	})
}

func TestClient_ReadCursor(t *testing.T) {
	ctx := context.Background()

	client, err := NewClient(standaloneMongoUri, "quasar_read_cursor_test", "source", "target")
	require.NoError(t, err)
	defer client.Close()

	require.NoError(t, client.sourceCollection.Drop(ctx))
	_, err = client.sourceCollection.InsertMany(ctx, []any{bson.M{"_id": "sub-1"}, bson.M{"_id": "sub-2"}, bson.M{"_id": "sub-3"}})
	require.NoError(t, err)

	t.Run("FailureMidIteration", func(t *testing.T) {
		cursor, err := client.sourceCollection.Aggregate(ctx, aggregateSnapshot, options.Aggregate().SetBatchSize(1))
		require.NoError(t, err)
		defer cursor.Close(ctx)

		// Kill the server-side cursor so the getMore after the first batch fails.
		killCursors := bson.D{
			{Key: "killCursors", Value: client.sourceCollection.Name()},
			{Key: "cursors", Value: bson.A{cursor.ID()}},
		}
		require.NoError(t, client.sourceCollection.Database().RunCommand(ctx, killCursors).Err())

		bulk, err := client.readCursor(cursor, primitive.NewObjectID())
		assert.Error(t, err, "a cursor failing mid-iteration must not yield a partial snapshot")
		assert.Len(t, bulk, 1)
	})
}

// TestClient_ReplicaSet provisions a single shared MongoDB replicaset and runs all replicaset-dependent
// scenarios as sub-tests so the container is only started once.
func TestClient_ReplicaSet(t *testing.T) {
	uri := test.SetupMongoReplicaSet(t)

	var client *Client
	var firstResumeToken bson.Raw

	t.Run("ConnectAndReadStream", func(t *testing.T) {
		var err error
		client, err = NewClient(uri, "quasar_replicaset_test", "source", "target")
		require.NoError(t, err)

		require.NoError(t, client.Connect(nil))

		doc := bson.M{"_id": "doc-1", "value": "first"}
		_, err = client.sourceCollection.InsertOne(context.Background(), doc)
		require.NoError(t, err)

		select {
		case payload := <-client.streamChan:
			var token bson.M
			require.NoError(t, bson.Unmarshal(payload, &token))
			assert.NotEmpty(t, token["_data"], "the stream should only pass the resume token of the event")

			firstResumeToken = payload

		case <-time.After(streamTimeout):
			t.Fatal("timed out waiting for change stream event")
		}
	})

	t.Run("ResumeToken", func(t *testing.T) {
		require.NotEmpty(t, firstResumeToken, "resume token should have been captured from the first event")

		doc := bson.M{"_id": "doc-2", "value": "second"}
		_, err := client.sourceCollection.InsertOne(context.Background(), doc)
		require.NoError(t, err)

		client.Close()

		resumedClient, err := NewClient(uri, "quasar_replicaset_test", "source", "target")
		require.NoError(t, err)
		defer resumedClient.Close()

		require.NoError(t, resumedClient.Connect(firstResumeToken))

		select {
		case payload := <-resumedClient.streamChan:
			// Only the doc-2 insert happened after the first token, so any delivered token belongs to it.
			var token bson.M
			require.NoError(t, bson.Unmarshal(payload, &token))
			assert.NotEmpty(t, token["_data"])
			assert.NotEqual(t, firstResumeToken, payload)

		case <-time.After(streamTimeout):
			t.Fatal("timed out waiting for resumed change stream event")
		}
	})

	t.Run("UnusableResumeToken", func(t *testing.T) {
		require.NotEmpty(t, firstResumeToken, "resume token should have been captured from the first event")

		// A token from another collection is never found in this collection's history (ChangeStreamFatalError).
		otherClient, err := NewClient(uri, "quasar_replicaset_test", "other_source", "target")
		require.NoError(t, err)
		defer otherClient.Close()

		// The server only rejects the token once the collection has an event newer than the token.
		// w=3 ensures the nearest member, which may be any secondary, already has that event.
		allMembers := otherClient.sourceCollection.Database().Collection("other_source",
			options.Collection().SetWriteConcern(&writeconcern.WriteConcern{W: 3}))
		_, err = allMembers.InsertOne(context.Background(), bson.M{"_id": "doc-3"})
		require.NoError(t, err)

		_, err = otherClient.sourceCollection.Watch(
			context.Background(),
			mongo.Pipeline{},
			options.ChangeStream().SetResumeAfter(firstResumeToken),
		)
		require.True(t, isResumeTokenErr(err), "precondition: the server must reject the token, got %v", err)

		require.NoError(t, otherClient.Connect(firstResumeToken), "an unusable resume token must fall back to a fresh stream")

		_, err = otherClient.sourceCollection.InsertOne(context.Background(), bson.M{"_id": "doc-4"})
		require.NoError(t, err)

		select {
		case payload := <-otherClient.streamChan:
			assert.NotEqual(t, firstResumeToken, payload)

		case <-time.After(streamTimeout):
			t.Fatal("timed out waiting for change stream event after resume token fallback")
		}
	})

	t.Run("Close", func(t *testing.T) {
		closeClient, err := NewClient(uri, "quasar_replicaset_test", "source", "target")
		require.NoError(t, err)
		require.NoError(t, closeClient.Connect(nil))

		done := make(chan struct{})
		go func() {
			closeClient.Close()
			close(done)
		}()

		select {
		case <-done:
			// Close returned promptly, confirming readStream exited and streamWg.Wait() was released.
		case <-time.After(streamTimeout):
			t.Fatal("Close did not return in time, readStream goroutine may have leaked")
		}
	})

	t.Run("CreateSnapshot", func(t *testing.T) {
		snapshotClient, err := NewClient(uri, "quasar_snapshot_test", "snapshot_source", "snapshot_target")
		require.NoError(t, err)

		sourceDocs := []any{
			bson.M{
				"_id":        "sub-1",
				"kind":       "Subscription",
				"apiVersion": "v1",
				"metadata":   bson.M{"name": "sub-1"},
				"spec":       bson.M{"foo": "bar"},
			},
			bson.M{
				"_id":        "sub-2",
				"kind":       "Subscription",
				"apiVersion": "v1",
				"metadata":   bson.M{"name": "sub-2"},
				"spec":       bson.M{"foo": "baz"},
			},
		}
		_, err = snapshotClient.sourceCollection.InsertMany(context.Background(), sourceDocs)
		require.NoError(t, err)

		snapshotId, insertedCount, err := snapshotClient.CreateSnapshot()
		require.NoError(t, err)
		assert.NotEmpty(t, snapshotId)
		assert.Equal(t, int64(len(sourceDocs)), insertedCount)

		_, err = primitive.ObjectIDFromHex(snapshotId)
		require.NoError(t, err, "snapshotId should be an ObjectId in hex format")

		// Read from the primary to avoid replication lag against the just-committed transaction.
		primaryTargetCollection := snapshotClient.targetCollection.Database().Collection(
			snapshotClient.targetCollection.Name(), options.Collection().SetReadPreference(readpref.Primary()))

		cursor, err := primaryTargetCollection.Find(context.Background(), bson.M{"snapshotId": snapshotId})
		require.NoError(t, err)

		var results []bson.M
		require.NoError(t, cursor.All(context.Background(), &results))
		require.Len(t, results, len(sourceDocs))

		for _, result := range results {
			assert.Equal(t, snapshotId, result["snapshotId"])
			assert.NotEmpty(t, result["subscriptionId"])

			resource, ok := result["resource"].(bson.M)
			require.True(t, ok, "resource should be present in the snapshot document")
			assert.Equal(t, "Subscription", resource["kind"])
			assert.Equal(t, "v1", resource["apiVersion"])
		}
	})
}
