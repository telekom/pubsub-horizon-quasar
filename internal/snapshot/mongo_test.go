// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/test"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func TestMongoStoreConcerns(t *testing.T) {
	c := testConfig()
	store := newMongoStore(&mongo.Client{}, c)
	require.Equal(t, c.Database, store.database.Name())
	require.Equal(t, c.OperationTimeout, store.database.WriteConcern().WTimeout)
	require.Equal(t, "majority", store.database.WriteConcern().W)
	require.True(t, *store.database.WriteConcern().Journal)
	require.Equal(t, "majority", store.database.ReadConcern().Level)
	require.Equal(t, readpref.PrimaryMode, store.database.ReadPreference().Mode())
	require.Equal(t, c.SourceCollection, store.source.Name())
	require.Equal(t, c.SnapshotCollection, store.snapshots.Name())
	require.Equal(t, c.HeadCollection, store.heads.Name())
	require.Equal(t, bson.D{
		{Key: "w", Value: "majority"}, {Key: "j", Value: true}, {Key: "wtimeout", Value: c.OperationTimeout.Milliseconds()},
	}, store.writeConcern)
}

func mongoFixture(t *testing.T, uri string) (*mongo.Client, *mongoStore, *worker) {
	t.Helper()
	c := testConfig()
	c.URI = uri
	c.Database = "snapshots_" + primitive.NewObjectID().Hex()
	c.OperationTimeout = 20 * time.Second
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

func TestMongoIntegration(t *testing.T) {
	uri := test.SetupMongoReplicaSet(t)
	t.Run("independent client and shutdown", func(t *testing.T) { testMongoServiceLifecycle(t, uri) })
	t.Run("setup retry after successful connection", func(t *testing.T) { testMongoSetupRetry(t, uri) })
	t.Run("persistent session", func(t *testing.T) { testMongoSession(t, uri) })
	t.Run("publication and schema", func(t *testing.T) { testMongoPublication(t, uri) })
	t.Run("missing source and head", func(t *testing.T) { testMongoMissingMetadata(t, uri) })
	t.Run("repair and invalid history", func(t *testing.T) { testMongoRepair(t, uri) })
	t.Run("existing validators", func(t *testing.T) { testMongoExistingValidators(t, uri) })
	t.Run("crash recovery", func(t *testing.T) { testMongoCrashRecovery(t, uri) })
	t.Run("interrupted cleanup", func(t *testing.T) { testMongoInterruptedCleanup(t, uri) })
	t.Run("replica set", func(t *testing.T) {
		t.Run("delayed visibility", func(t *testing.T) { testMongoDelayedVisibility(t, uri) })
		t.Run("inflight shutdown", func(t *testing.T) { testMongoInflightShutdown(t, uri) })
		t.Run("reader retries retirement", func(t *testing.T) { testMongoReaderRetirement(t, uri) })
		t.Run("majority and uncertain writes", func(t *testing.T) { testMongoUncertainWrites(t, uri) })
		t.Run("primary failover", func(t *testing.T) { testMongoFailover(t, uri) })
	})
}

func testMongoSession(t *testing.T, uri string) {
	client, store, w := mongoFixture(t, uri)
	s := &service{client: client, store: store}
	t.Cleanup(func() {
		if s.session != nil {
			s.session.EndSession(context.Background())
		}
	})
	require.NoError(t, s.withSession(t.Context(), func(ctx context.Context) error {
		require.NotNil(t, mongo.SessionFromContext(ctx))
		require.Same(t, s.session, mongo.SessionFromContext(ctx))
		return w.refresh(ctx)
	}))
	session := s.session
	require.NotNil(t, session.OperationTime())
	require.NoError(t, s.withSession(t.Context(), func(ctx context.Context) error {
		require.Same(t, session, mongo.SessionFromContext(ctx), "refresh and cleanup must reuse the same session")
		return w.cleanup(ctx, time.Now())
	}))
}

func testMongoServiceLifecycle(t *testing.T, uri string) {
	const password = "test-p@ss:/?#%"
	existingClient, store, w := mongoFixture(t, uri)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	require.NoError(t, store.database.RunCommand(ctx, bson.D{
		{Key: "createUser", Value: "snapshot-writer"},
		{Key: "pwd", Value: password},
		{Key: "roles", Value: bson.A{"dbOwner"}},
	}).Err())
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, store.database.RunCommand(cleanupContext,
			bson.D{{Key: "dropUser", Value: "snapshot-writer"}}).Err())
	})
	c := w.config
	authenticatedURI, err := url.Parse(c.URI)
	require.NoError(t, err)
	authenticatedURI.Path = "/"
	authenticatedURI.User = url.UserPassword("snapshot-writer", password)
	query := authenticatedURI.Query()
	query.Set("authSource", c.Database)
	authenticatedURI.RawQuery = query.Encode()
	c.URI = authenticatedURI.String()
	require.NoError(t, c.Validate())
	s := newService(c)
	t.Cleanup(s.shutdown)
	go s.run()
	require.Eventually(t, func() bool {
		count, err := store.heads.CountDocuments(ctx, bson.D{{Key: "documentCount", Value: int64(3)}})
		return err == nil && count == 1
	}, 5*time.Second, 10*time.Millisecond)
	s.shutdown()
	require.NotSame(t, existingClient, s.client)
	require.NoError(t, existingClient.Ping(ctx, nil), "the independent worker must not close existing clients")
	require.NoError(t, store.setup(ctx), "shutdown must preserve the published collections")
	t.Run("invalid authentication is fatal", func(t *testing.T) {
		authenticatedURI.User = url.UserPassword("snapshot-writer", "wrong-auth-secret")
		output := requireSnapshotConnectionFatal(t, authenticatedURI.String(), "ping", "snapshot-writer", "wrong-auth-secret")
		require.NotContains(t, output, "(timeout)", "authentication must fail against the reachable replica set")
	})
}

func testMongoSetupRetry(t *testing.T, uri string) {
	_, store, w := mongoFixture(t, uri)
	require.NoError(t, store.source.Drop(t.Context()))
	s := newService(w.config)
	t.Cleanup(func() {
		s.cancel()
		s.disconnect()
	})
	ctx, cancel := context.WithTimeout(t.Context(), w.config.OperationTimeout)
	defer cancel()
	require.ErrorContains(t, s.initialize(ctx), "required subscription snapshot collection is missing")
	require.NotNil(t, s.client, "connect and ping succeeded before setup failed")
	require.Nil(t, s.worker)
	client := s.client
	_, err := store.source.InsertOne(ctx, sourceDocument(t, "a", "restored"))
	require.NoError(t, err)
	require.NoError(t, s.initialize(ctx))
	require.Same(t, client, s.client, "setup retry must reuse the connected client")
	require.NoError(t, s.withSession(ctx, s.worker.refresh))
}

func testMongoPublication(t *testing.T, uri string) {
	_, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	require.NoError(t, w.refresh(ctx))
	first, err := store.readHead(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), first.Version.DocumentCount)
	require.NoError(t, w.refresh(ctx))
	unchanged, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(first, unchanged))
	document, err := store.snapshots.FindOne(ctx, bson.D{
		{Key: "snapshotId", Value: first.Version.SnapshotID}, {Key: "subscriptionId", Value: "a"},
	}).Raw()
	require.NoError(t, err)
	require.Equal(t, bson.TypeObjectID, document.Lookup("_id").Type)
	require.Equal(t, bson.TypeString, document.Lookup("snapshotId").Type)
	require.Equal(t, "first", document.Lookup("resource", "spec", "value").StringValue())
	require.Zero(t, document.Lookup("resource", "_id").Type)
	indexes, err := store.snapshots.Indexes().ListSpecifications(ctx)
	require.NoError(t, err)
	require.Len(t, indexes, 3)
	for _, collection := range []*mongo.Collection{store.source, store.heads} {
		collectionIndexes, err := collection.Indexes().ListSpecifications(ctx)
		require.NoError(t, err)
		require.Len(t, collectionIndexes, 1)
	}
	var duplicate bson.M
	require.NoError(t, bson.Unmarshal(document, &duplicate))
	duplicate["_id"] = primitive.NewObjectID()
	_, err = store.snapshots.InsertOne(ctx, duplicate)
	require.True(t, mongo.IsDuplicateKeyError(err))
	duplicate["snapshotId"] = primitive.NewObjectID()
	_, err = store.snapshots.InsertOne(ctx, duplicate)
	require.Error(t, err, "native ObjectID snapshotId must fail schema validation")
	_, err = store.source.DeleteMany(ctx, bson.D{})
	require.NoError(t, err)
	require.NoError(t, w.refresh(ctx))
	empty, err := store.readHead(ctx)
	require.NoError(t, err)
	require.Zero(t, empty.Version.DocumentCount)
	require.Equal(t, first.Version, empty.RecentSnapshots[1])
	restarted := newWorker(w.config, store)
	require.NoError(t, restarted.refresh(ctx))
	afterRestart, err := store.readHead(ctx)
	require.NoError(t, err)
	require.NotEqual(t, empty.Version.SnapshotID, afterRestart.Version.SnapshotID)
	require.Len(t, afterRestart.RecentSnapshots, 3)
	require.NoError(t, store.setup(ctx), "schema/index setup must be idempotent")
}

func testMongoMissingMetadata(t *testing.T, uri string) {
	_, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	require.NoError(t, w.refresh(ctx))
	before, err := store.readHead(ctx)
	require.NoError(t, err)
	require.NoError(t, store.source.Drop(ctx))
	require.Error(t, w.refresh(ctx), "missing source must not publish an empty snapshot")
	after, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(before, after))
	_, err = store.heads.DeleteMany(ctx, bson.D{})
	require.NoError(t, err)
	require.Error(t, w.refresh(ctx))
	store.seenHead = false
	require.Error(t, newWorker(w.config, store).refresh(ctx), "restart cannot invent lost history from snapshot rows")
	require.Error(t, w.cleanup(ctx, time.Now().Add(30*24*time.Hour)))
}

func testMongoRepair(t *testing.T, uri string) {
	_, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	require.NoError(t, w.refresh(ctx))
	before, err := store.readHead(ctx)
	require.NoError(t, err)
	_, err = store.snapshots.DeleteOne(ctx, bson.D{{Key: "snapshotId", Value: before.Version.SnapshotID}})
	require.NoError(t, err)
	require.NoError(t, w.refresh(ctx))
	repaired, err := store.readHead(ctx)
	require.NoError(t, err)
	require.Equal(t, before.Version.SourceHash, repaired.Version.SourceHash)
	require.NotEqual(t, before.Version.SnapshotID, repaired.Version.SnapshotID)
	require.Equal(t, before.Version, repaired.RecentSnapshots[1])
	require.Error(t, w.cleanup(ctx, time.Now()), "damaged predecessor must not be silently replaced by an orphan")
	_, err = store.heads.UpdateOne(ctx, bson.D{{Key: "_id", Value: "head"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "documentCount", Value: int32(3)}}}},
		options.Update().SetBypassDocumentValidation(true))
	require.NoError(t, err)
	require.Error(t, w.refresh(ctx), "BSON int32 does not satisfy the head contract")
	require.Error(t, w.cleanup(ctx, time.Now()))
}

func testMongoExistingValidators(t *testing.T, uri string) {
	_, store, _ := mongoFixture(t, uri)
	ctx := t.Context()
	require.NoError(t, store.snapshots.Drop(ctx))
	require.NoError(t, store.database.CreateCollection(ctx, store.snapshots.Name()))
	require.NoError(t, store.setup(ctx), "missing validator may be installed on the new collection")
	validator := bson.M{"$jsonSchema": bson.M{"bsonType": "object", "required": bson.A{"other"}}}
	require.NoError(t, store.database.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: store.snapshots.Name()}, {Key: "validator", Value: validator},
	}).Err())
	require.ErrorContains(t, store.setup(ctx), "manual migration")
	spec, err := store.collectionSpec(ctx, store.snapshots.Name())
	require.NoError(t, err)
	require.Equal(t, "other", spec.Options.Lookup("validator", "$jsonSchema", "required", "0").StringValue())
}

func testMongoReaderRetirement(t *testing.T, uri string) {
	client, store, w := mongoFixture(t, uri)
	require.NoError(t, w.refresh(t.Context()))
	pinned, err := store.readHead(t.Context())
	require.NoError(t, err)
	attempts := 0
	session, err := client.StartSession(options.Session().SetCausalConsistency(true))
	require.NoError(t, err)
	defer session.EndSession(t.Context())
	err = mongo.WithSession(t.Context(), session, func(ctx mongo.SessionContext) error {
		for range 3 {
			attempts++
			current, err := store.readHead(ctx)
			if err != nil {
				return err
			}
			complete, err := readCompleteSnapshot(ctx, store, current, func() {
				if attempts != 1 {
					return
				}
				for range 3 {
					require.NoError(t, newWorker(w.config, store).refresh(t.Context()))
				}
				require.NoError(t, w.cleanup(t.Context(), time.Now().Add(8*24*time.Hour)))
			})
			if err != nil {
				return err
			}
			if !complete {
				continue
			}
			require.Equal(t, int64(3), current.Version.DocumentCount)
			require.NotEqual(t, pinned.Version.SnapshotID, current.Version.SnapshotID)
			return nil
		}
		return errors.New("reader exhausted its retry budget")
	})
	require.NoError(t, err)
	require.Equal(t, 2, attempts, "retired partial results must be discarded")
}

func readCompleteSnapshot(ctx context.Context, store *mongoStore, current head, afterFirst func()) (bool, error) {
	documents, err := loadSnapshot(ctx, store, current.Version.SnapshotID, afterFirst)
	if err != nil {
		return false, err
	}
	count, err := store.countSnapshot(ctx, current.Version.SnapshotID)
	if err != nil {
		return false, err
	}
	latest, err := store.readHead(ctx)
	if err != nil {
		return false, err
	}
	return count == current.Version.DocumentCount && int64(len(documents)) == count &&
		containsSnapshot(latest, current.Version.SnapshotID), nil
}

func loadSnapshot(ctx context.Context, store *mongoStore, id string, afterFirst func()) ([]bson.Raw, error) {
	cursor, err := store.snapshots.Find(ctx, bson.D{{Key: "snapshotId", Value: id}}, options.Find().SetBatchSize(1))
	if err != nil {
		return nil, err
	}
	defer closeCursor(ctx, cursor)
	var documents []bson.Raw
	for cursor.Next(ctx) {
		documents = append(documents, slices.Clone(cursor.Current))
		if len(documents) == 1 {
			afterFirst()
		}
	}
	return documents, cursor.Err()
}

func setFailCommand(t *testing.T, client *mongo.Client, data bson.D) {
	t.Helper()
	require.NoError(t, client.Database("admin").RunCommand(t.Context(), bson.D{
		{Key: "configureFailPoint", Value: "failCommand"},
		{Key: "mode", Value: bson.D{{Key: "times", Value: 1}}},
		{Key: "data", Value: data},
	}).Err())
}

func testMongoUncertainWrites(t *testing.T, uri string) {
	client, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	require.Equal(t, "majority", store.database.ReadConcern().Level)
	require.Equal(t, "majority", store.database.WriteConcern().W)
	require.True(t, *store.database.WriteConcern().Journal)
	s := &service{client: client, store: store}
	require.NoError(t, s.withSession(ctx, w.refresh))
	old, err := store.readHead(ctx)
	require.NoError(t, err)
	_, err = store.source.ReplaceOne(ctx, bson.D{{Key: "_id", Value: "a"}}, sourceDocument(t, "a", "changed"))
	require.NoError(t, err)
	setFailCommand(t, client, bson.D{
		{Key: "failCommands", Value: bson.A{"insert"}}, {Key: "errorCode", Value: 121},
	})
	require.Error(t, s.withSession(ctx, w.refresh))
	require.Nil(t, w.pending, "failed insertion batches must not send an activation")
	unchanged, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(old, unchanged))
	setFailCommand(t, client, bson.D{
		{Key: "failCommands", Value: bson.A{"update"}},
		{Key: "writeConcernError", Value: bson.D{{Key: "code", Value: 64}, {Key: "errmsg", Value: "test lost acknowledgement"}}},
	})
	require.Error(t, s.withSession(ctx, w.refresh))
	require.NotNil(t, w.pending)
	candidate := w.pending.next
	require.Error(t, w.cleanup(ctx, time.Now().Add(30*24*time.Hour)))
	require.NoError(t, s.withSession(ctx, w.refresh))
	confirmed, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(candidate, confirmed))
	require.Len(t, confirmed.RecentSnapshots, 2)
}

func testMongoFailover(t *testing.T, uri string) {
	client, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	s := &service{client: client, store: store}
	require.NoError(t, s.withSession(ctx, w.refresh))
	before, err := store.readHead(ctx)
	require.NoError(t, err)
	var hello struct {
		Primary string `bson:"primary"`
	}
	require.NoError(t, client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello))
	previousPrimary := hello.Primary
	direct, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://"+previousPrimary+"/?directConnection=true"))
	require.NoError(t, err)
	defer func() { require.NoError(t, direct.Disconnect(context.Background())) }()
	// Stepdown may sever the command's connection after the server has applied it.
	_ = direct.Database("admin").RunCommand(ctx, bson.D{
		{Key: "replSetStepDown", Value: 60}, {Key: "force", Value: true},
	}).Err()
	require.Eventually(t, func() bool {
		err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello)
		return err == nil && hello.Primary != "" && hello.Primary != previousPrimary
	}, 60*time.Second, 100*time.Millisecond)
	_, err = store.source.ReplaceOne(ctx, bson.D{{Key: "_id", Value: "a"}}, sourceDocument(t, "a", "after failover"))
	require.NoError(t, err)
	require.NoError(t, s.withSession(ctx, w.refresh))
	after, err := store.readHead(ctx)
	require.NoError(t, err)
	require.Equal(t, before.Version, after.RecentSnapshots[1])
}
