// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/test"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// configureTestFailpoint enables a MongoDB fault and returns its entry count and a cleanup-safe release function.
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

// waitForTestFailpoint waits until MongoDB enters the configured fault at least once after the saved count.
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

// pauseSecondaryReplication stops apply on both secondaries and returns a function that resumes replication.
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

// testMongoDelayedVisibility checks that locally inserted rows cannot advance the head before majority acknowledgement.
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
	// Local reads expose the blocked write, while majority reads must still see the old publication.
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

// testMongoInflightShutdown checks cancellation of a blocked database operation and isolation from unrelated clients.
func testMongoInflightShutdown(t *testing.T, uri string) {
	existingClient, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	c := w.config
	c.RefreshTimeout = 2 * time.Minute
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

// TestMongoStoreConcerns checks dedicated collection settings for primary reads and journaled majority writes.
func TestMongoStoreConcerns(t *testing.T) {
	c := testConfig()
	store := newMongoStore(&mongo.Client{}, c)
	require.Equal(t, c.Database, store.database.Name())
	require.Zero(t, store.database.WriteConcern().WTimeout, "write-concern timeout uses MongoDB defaults")
	require.Equal(t, "majority", store.database.WriteConcern().W)
	require.True(t, *store.database.WriteConcern().Journal)
	require.Equal(t, "majority", store.database.ReadConcern().Level)
	require.Equal(t, readpref.PrimaryMode, store.database.ReadPreference().Mode())
	require.Equal(t, c.SourceCollection, store.source.Name())
	require.Equal(t, c.SnapshotCollection, store.snapshots.Name())
	require.Equal(t, c.HeadCollection, store.heads.Name())
}

// TestMongoIntegration runs snapshot storage, recovery and failover checks against an isolated three-member replica set.
func TestMongoIntegration(t *testing.T) {
	uri := test.SetupMongoReplicaSet(t)
	t.Run("independent client and shutdown", func(t *testing.T) { testMongoServiceLifecycle(t, uri) })
	t.Run("setup retry after successful connection", func(t *testing.T) { testMongoSetupRetry(t, uri) })
	t.Run("result durations exclude initialization", func(t *testing.T) { testMongoResultInitializationDuration(t, uri) })
	t.Run("persistent session", func(t *testing.T) { testMongoSession(t, uri) })
	t.Run("publication and schema", func(t *testing.T) { testMongoPublication(t, uri) })
	t.Run("missing source and head", func(t *testing.T) { testMongoMissingMetadata(t, uri) })
	t.Run("repair and invalid history", func(t *testing.T) { testMongoRepair(t, uri) })
	t.Run("existing validators", func(t *testing.T) { testMongoExistingValidators(t, uri) })
	t.Run("source collection types", func(t *testing.T) { testMongoSourceCollections(t, uri) })
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

// testMongoResultInitializationDuration checks that schema setup time is excluded from snapshot result durations.
func testMongoResultInitializationDuration(t *testing.T, uri string) {
	client, store, w := mongoFixture(t, uri)
	_, disable := configureTestFailpoint(t, client, "failCommand", bson.D{{Key: "times", Value: 1}}, bson.D{
		{Key: "failCommands", Value: bson.A{"create"}},
		{Key: "blockConnection", Value: true},
		{Key: "blockTimeMS", Value: 1000},
	})
	defer disable()
	var output bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&output)
	t.Cleanup(func() { log.Logger = previous })
	s := newService(w.config)
	defer s.cancel()
	s.client, s.store = client, store
	defer func() {
		if s.session != nil {
			s.session.EndSession(t.Context())
		}
	}()
	started := time.Now()
	s.attemptRefresh()
	elapsed := time.Since(started)
	require.NotNil(t, s.worker)
	require.False(t, s.worker.lastSuccess.IsZero())
	var createdEntry, publicationEntry map[string]any
	var createdLogs, publicationLogs int
	for _, entry := range serviceLogMessages(t, output.Bytes()) {
		switch entry["message"] {
		case "Subscription snapshot created":
			createdEntry = entry
			createdLogs++
		case "Subscription snapshot published":
			publicationEntry = entry
			publicationLogs++
		}
	}
	require.Equal(t, 1, createdLogs)
	require.Equal(t, 1, publicationLogs)
	require.NotNil(t, createdEntry)
	require.NotNil(t, publicationEntry)
	createdDuration, createdOK := createdEntry["durationMs"].(float64)
	publicationDuration, publicationOK := publicationEntry["durationMs"].(float64)
	require.True(t, createdOK)
	require.True(t, publicationOK)
	require.GreaterOrEqual(t, float64(elapsed)/float64(time.Millisecond), publicationDuration+1000,
		"schema initialization is outside the snapshot and publication durations")
	require.GreaterOrEqual(t, publicationDuration, createdDuration)
	require.Equal(t, "info", createdEntry["level"])

	output.Reset()
	started = time.Now()
	s.attemptRefresh()
	elapsed = time.Since(started)
	entries := serviceLogMessages(t, output.Bytes())
	require.Equal(t, []string{"Subscription snapshot source unchanged"}, messageNames(entries))
	require.Equal(t, "debug", entries[0]["level"])
	unchangedDuration, ok := entries[0]["durationMs"].(float64)
	require.True(t, ok)
	require.LessOrEqual(t, unchangedDuration, float64(elapsed)/float64(time.Millisecond))
}

// testMongoSession checks that refresh and cleanup reuse the same causally consistent MongoDB session.
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
		return w.cleanup(ctx)
	}))
}

// testMongoServiceLifecycle checks authenticated startup, isolated shutdown and fatal authentication failure logging.
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
	base, rawQuery, _ := strings.Cut(c.URI, "?")
	query, err := url.ParseQuery(rawQuery)
	require.NoError(t, err)
	query.Set("authSource", c.Database)
	authenticatedURI := func(password string) string {
		return strings.Replace(base, "mongodb://", "mongodb://"+url.UserPassword("snapshot-writer", password).String()+"@", 1) +
			"?" + query.Encode()
	}
	c.URI = authenticatedURI(password)
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
		output := requireSnapshotConnectionFatal(t, authenticatedURI("wrong-auth-secret"), "ping", "snapshot-writer", "wrong-auth-secret")
		require.NotContains(t, output, "(timeout)", "authentication must fail against the reachable replica set")
	})
}

// testMongoSetupRetry checks that restoring a missing source lets setup retry reuse the existing connection.
func testMongoSetupRetry(t *testing.T, uri string) {
	_, store, w := mongoFixture(t, uri)
	require.NoError(t, store.source.Drop(t.Context()))
	s := newService(w.config)
	t.Cleanup(func() {
		s.cancel()
		s.disconnect()
	})
	ctx, cancel := context.WithTimeout(t.Context(), w.config.RefreshTimeout)
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

// testMongoPublication checks publication, indexes, schema enforcement, empty sources and restart history.
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

// testMongoMissingMetadata checks that missing source or head data cannot silently reset publication history.
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
	require.Error(t, w.cleanup(ctx))
}

// testMongoRepair checks snapshot repair while damaged retained history continues to block cleanup.
func testMongoRepair(t *testing.T, uri string) {
	_, store, w := mongoFixture(t, uri)
	ctx := t.Context()
	require.NoError(t, w.refresh(ctx))
	before, err := store.readHead(ctx)
	require.NoError(t, err)
	_, err = store.snapshots.DeleteOne(ctx, bson.D{{Key: "snapshotId", Value: before.Version.SnapshotID}})
	require.NoError(t, err)
	require.ErrorContains(t, w.refresh(ctx), "protected snapshot is incomplete")
	repaired, err := store.readHead(ctx)
	require.NoError(t, err)
	require.Equal(t, before.Version.SourceHash, repaired.Version.SourceHash)
	require.NotEqual(t, before.Version.SnapshotID, repaired.Version.SnapshotID)
	require.Equal(t, before.Version, repaired.RecentSnapshots[1])
	require.Error(t, w.cleanup(ctx), "damaged predecessor must not be silently replaced by an orphan")
	_, err = store.heads.UpdateOne(ctx, bson.D{{Key: "_id", Value: "head"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "documentCount", Value: int32(3)}}}},
		options.Update().SetBypassDocumentValidation(true))
	require.NoError(t, err)
	require.Error(t, w.refresh(ctx), "BSON int32 does not satisfy the head contract")
	require.Error(t, w.cleanup(ctx))
}

// testMongoExistingValidators checks that setup rejects incompatible collection options without changing them.
func testMongoExistingValidators(t *testing.T, uri string) {
	tests := []struct {
		name   string
		change func(*options.CreateCollectionOptions)
	}{
		{"missing validator", func(o *options.CreateCollectionOptions) { o.Validator = nil }},
		{"different validator", func(o *options.CreateCollectionOptions) {
			o.SetValidator(bson.M{"$jsonSchema": bson.M{"bsonType": "object", "required": bson.A{"other"}}})
		}},
		{"validation disabled", func(o *options.CreateCollectionOptions) { o.SetValidationLevel("off") }},
		{"validation warning", func(o *options.CreateCollectionOptions) { o.SetValidationAction("warn") }},
		{"different collation", func(o *options.CreateCollectionOptions) { o.SetCollation(&options.Collation{Locale: "en"}) }},
		{"capped", func(o *options.CreateCollectionOptions) { o.SetCapped(true).SetSizeInBytes(65536) }},
	}
	for _, target := range []string{"snapshots", "head"} {
		t.Run(target, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					_, store, _ := mongoFixture(t, uri)
					ctx := t.Context()
					collection, validator := store.snapshots, snapshotValidator()
					if target == "head" {
						collection, validator = store.heads, headValidator()
					}
					require.NoError(t, collection.Drop(ctx))
					opts := options.CreateCollection().SetValidator(validator).SetValidationLevel("strict").
						SetValidationAction("error").SetCollation(&options.Collation{Locale: "simple"})
					tt.change(opts)
					require.NoError(t, store.database.CreateCollection(ctx, collection.Name(), opts))
					filter := bson.D{{Key: "name", Value: collection.Name()}}
					before, err := store.database.ListCollectionSpecifications(ctx, filter)
					require.NoError(t, err)
					require.ErrorContains(t, store.setup(ctx), "manual migration")
					after, err := store.database.ListCollectionSpecifications(ctx, filter)
					require.NoError(t, err)
					require.Equal(t, before, after, "setup must not alter an incompatible collection")
				})
			}
		})
	}
}

// testMongoSourceCollections checks that views, time-series and capped sources fail without advancing the head.
func testMongoSourceCollections(t *testing.T, uri string) {
	tests := []struct {
		name   string
		create func(context.Context, *mongoStore) error
	}{
		{"view", func(ctx context.Context, store *mongoStore) error {
			return store.database.CreateView(ctx, store.source.Name(), store.snapshots.Name(), mongo.Pipeline{})
		}},
		{"timeseries", func(ctx context.Context, store *mongoStore) error {
			return store.database.CreateCollection(ctx, store.source.Name(),
				options.CreateCollection().SetTimeSeriesOptions(options.TimeSeries().SetTimeField("time")))
		}},
		{"capped", func(ctx context.Context, store *mongoStore) error {
			return store.database.CreateCollection(ctx, store.source.Name(),
				options.CreateCollection().SetCapped(true).SetSizeInBytes(65536))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, store, w := mongoFixture(t, uri)
			ctx := t.Context()
			require.NoError(t, w.refresh(ctx))
			before, err := store.readHead(ctx)
			require.NoError(t, err)
			require.NoError(t, store.source.Drop(ctx))
			require.NoError(t, tt.create(ctx, store))
			require.ErrorContains(t, store.setup(ctx), "ordinary")
			require.ErrorContains(t, w.refresh(ctx), "ordinary")
			after, err := store.readHead(ctx)
			require.NoError(t, err)
			require.True(t, sameHead(before, after))
		})
	}
}

// testMongoReaderRetirement checks that a reader discards a retired partial snapshot and retries from the latest head.
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
				// Retire the pinned version while its cursor is open to force a safe reader retry.
				for range 3 {
					require.NoError(t, newWorker(w.config, store).refresh(t.Context()))
				}
				require.NoError(t, w.cleanup(t.Context()))
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

// readCompleteSnapshot verifies loaded rows and current retention before accepting a reader's snapshot.
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

// loadSnapshot copies version rows in single-row batches and runs a hook after the first row.
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

// setFailCommand configures a one-shot MongoDB command fault with the supplied failure details.
func setFailCommand(t *testing.T, client *mongo.Client, data bson.D) {
	t.Helper()
	require.NoError(t, client.Database("admin").RunCommand(t.Context(), bson.D{
		{Key: "configureFailPoint", Value: "failCommand"},
		{Key: "mode", Value: bson.D{{Key: "times", Value: 1}}},
		{Key: "data", Value: data},
	}).Err())
}

// testMongoUncertainWrites checks failed inserts and recovery of a head update whose acknowledgement was lost.
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
	require.Nil(t, w.proposal, "failed insertion batches must not send an activation")
	unchanged, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(old, unchanged))
	setFailCommand(t, client, bson.D{
		{Key: "failCommands", Value: bson.A{"update"}},
		{Key: "writeConcernError", Value: bson.D{{Key: "code", Value: 64}, {Key: "errmsg", Value: "test lost acknowledgement"}}},
	})
	require.Error(t, s.withSession(ctx, w.refresh))
	require.NotNil(t, w.proposal)
	candidate := w.proposal.next
	require.Error(t, w.cleanup(ctx))
	require.NoError(t, s.withSession(ctx, w.refresh))
	confirmed, err := store.readHead(ctx)
	require.NoError(t, err)
	require.True(t, sameHead(candidate, confirmed))
	require.Len(t, confirmed.RecentSnapshots, 2)
}

// testMongoFailover checks that publication resumes on a new primary without losing the previous head history.
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
