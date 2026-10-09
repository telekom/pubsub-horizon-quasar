// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

type mongoStore struct {
	database  *mongo.Database
	source    *mongo.Collection
	snapshots *mongo.Collection
	heads     *mongo.Collection
	seenHead  bool
}

// newMongoStore binds the collections with primary, majority reads and journaled majority writes.
func newMongoStore(client *mongo.Client, c config.SubscriptionSnapshots) *mongoStore {
	journal := true
	wc := &writeconcern.WriteConcern{W: "majority", Journal: &journal}
	database := client.Database(c.Database, options.Database().
		SetReadPreference(readpref.Primary()).SetReadConcern(readconcern.Majority()).SetWriteConcern(wc))
	return &mongoStore{
		database: database, source: database.Collection(c.SourceCollection),
		snapshots: database.Collection(c.SnapshotCollection), heads: database.Collection(c.HeadCollection),
	}
}

// readHead validates publication metadata and bootstraps it only when no existing data would be lost.
func (m *mongoStore) readHead(ctx context.Context) (head, error) {
	raw, err := m.heads.FindOne(ctx, bson.D{{Key: fieldID, Value: headID}}).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return m.bootstrap(ctx)
	}
	if err != nil {
		return head{}, databaseError("read head", err)
	}
	current, err := decodeHead(raw)
	if err == nil {
		m.seenHead = true
	}
	return current, err
}

// bootstrap creates and reads back an empty head only for a previously unseen, empty snapshot store.
func (m *mongoStore) bootstrap(ctx context.Context) (head, error) {
	if m.seenHead {
		return head{}, errors.New("previously observed MongoDB head document is missing; " +
			"restore the original head document in the configured head collection")
	}
	for _, collection := range []*mongo.Collection{m.snapshots, m.heads} {
		count, err := collection.CountDocuments(ctx, bson.D{}, options.Count().SetLimit(1))
		if err != nil {
			return head{}, databaseError("check bootstrap state", err)
		}
		if count != 0 {
			return head{}, errors.New("MongoDB head document is missing alongside existing snapshot/head data; " +
				"restore the original head document in the configured head collection")
		}
	}
	_, err := m.heads.InsertOne(ctx, bson.D{
		{Key: fieldID, Value: headID}, {Key: fieldRecentSnapshots, Value: bson.A{}},
	})
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return head{}, databaseError("bootstrap head", err)
	}
	raw, err := m.heads.FindOne(ctx, bson.D{{Key: fieldID, Value: headID}}).Raw()
	if err != nil {
		return head{}, databaseError("confirm bootstrap head", err)
	}
	current, err := decodeHead(raw)
	if err == nil {
		m.seenHead = true
	}
	return current, err
}

// readSource buffers subscriptions in binary ID order and rejects invalid or oversized source data.
func (m *mongoStore) readSource(ctx context.Context, limit int64) (*sourceBuffer, error) {
	if err := m.requireSourceCollection(ctx); err != nil {
		return nil, err
	}
	cursor, err := m.source.Find(ctx, bson.D{}, options.Find().
		SetSort(bson.D{{Key: fieldID, Value: 1}}).SetCollation(&options.Collation{Locale: simpleCollation}).
		SetBatchSize(batchSize))
	if err != nil {
		return nil, databaseError("read source", err)
	}
	defer closeCursor(ctx, cursor)
	buffer := newSourceBuffer()
	for cursor.Next(ctx) {
		if err := buffer.add(cursor.Current, limit); err != nil {
			return nil, err
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, databaseError("finish source scan", err)
	}
	return buffer, nil
}

// insertSnapshot wraps buffered subscriptions and writes ordered batches limited by count and byte size.
func (m *mongoStore) insertSnapshot(ctx context.Context, id string, source *sourceBuffer) error {
	documents := make([]any, 0, batchSize)
	var size int
	flush := func() error {
		if len(documents) == 0 {
			return nil
		}
		_, err := m.snapshots.InsertMany(ctx, documents, options.InsertMany().SetOrdered(true))
		clear(documents)
		documents = documents[:0]
		size = 0
		return databaseError("insert snapshot batch", err)
	}
	for _, raw := range source.documents {
		document, err := wrapDocument(raw, id)
		if err != nil {
			return err
		}
		if len(documents) == batchSize || size+len(document) > writeBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		documents = append(documents, document)
		size += len(document)
	}
	return flush()
}

// activate replaces the head and history only if the previous version still matches.
func (m *mongoStore) activate(ctx context.Context, previous string, next head) (bool, error) {
	var expected any = previous
	if previous == "" {
		// The bootstrap head has no snapshotId field, rather than an empty string.
		expected = bson.D{{Key: "$exists", Value: false}}
	}
	result, err := m.heads.ReplaceOne(ctx, bson.D{
		{Key: fieldID, Value: headID}, {Key: fieldSnapshotID, Value: expected},
	}, next, options.Replace().SetUpsert(false))
	if err != nil {
		return false, databaseError("activate head", err)
	}
	return result.MatchedCount == 1, nil
}

// countSnapshot counts stored rows for one snapshot version using the store's read concern.
func (m *mongoStore) countSnapshot(ctx context.Context, id string) (int64, error) {
	count, err := m.snapshots.CountDocuments(ctx, bson.D{{Key: fieldSnapshotID, Value: id}})
	return count, databaseError("count snapshot", err)
}

// visitSnapshotIDs streams distinct version IDs to a callback and aborts on invalid IDs or callback errors.
func (m *mongoStore) visitSnapshotIDs(ctx context.Context, visit func(string) error) error {
	cursor, err := m.snapshots.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{{Key: fieldID, Value: "$snapshotId"}}}},
	}, options.Aggregate().SetAllowDiskUse(true).SetBatchSize(batchSize))
	if err != nil {
		return databaseError("list snapshot versions", err)
	}
	defer closeCursor(ctx, cursor)
	for cursor.Next(ctx) {
		id, ok := cursor.Current.Lookup(fieldID).StringValueOK()
		if !ok {
			return errors.New("snapshot collection contains a non-string snapshotId; cleanup aborted")
		}
		if err := visit(id); err != nil {
			return err
		}
	}
	return databaseError("finish listing snapshot versions", cursor.Err())
}

// deleteBatch selects and removes at most one batch of rows belonging to the given version.
func (m *mongoStore) deleteBatch(ctx context.Context, id string) (int64, error) {
	cursor, err := m.snapshots.Find(ctx, bson.D{{Key: fieldSnapshotID, Value: id}},
		options.Find().SetProjection(bson.D{{Key: fieldID, Value: 1}}).SetLimit(batchSize))
	if err != nil {
		return 0, databaseError("select snapshot deletion batch", err)
	}
	defer closeCursor(ctx, cursor)
	ids := make(bson.A, 0, batchSize)
	for cursor.Next(ctx) {
		id := cursor.Current.Lookup(fieldID)
		// Cursor storage is reused on the next row, so selected IDs must own their bytes.
		id.Value = slices.Clone(id.Value)
		ids = append(ids, id)
	}
	if err := cursor.Err(); err != nil {
		return 0, databaseError("finish snapshot deletion selection", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := m.snapshots.DeleteMany(ctx, bson.D{
		{Key: fieldSnapshotID, Value: id}, {Key: fieldID, Value: bson.D{{Key: "$in", Value: ids}}},
	})
	if err != nil {
		return 0, databaseError("delete snapshot batch", err)
	}
	return result.DeletedCount, nil
}

// closeCursor releases a MongoDB cursor and logs cleanup failures without exposing driver details.
func closeCursor(ctx context.Context, cursor *mongo.Cursor) {
	if err := cursor.Close(ctx); err != nil {
		log.Error().Err(databaseError("close cursor", err)).Msg("Subscription snapshot cursor cleanup failed")
	}
}

type mongoOperationError struct {
	operation string
	cause     error
}

// Error describes the failed operation with a server code or timeout, hiding sensitive driver messages.
func (e *mongoOperationError) Error() string {
	message := e.operation + " failed"
	var commandError mongo.CommandError
	if errors.As(e.cause, &commandError) {
		message += " (MongoDB code " + strconv.Itoa(int(commandError.Code)) + ")"
	}
	if mongo.IsTimeout(e.cause) {
		message += " (timeout)"
	}
	return message
}

// Unwrap exposes the original MongoDB error for error matching without changing the safe log message.
func (e *mongoOperationError) Unwrap() error {
	return e.cause
}

// databaseError wraps a non-nil MongoDB error with a safe operation label and preserves its cause.
func databaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	// Server/driver messages can contain URIs, credentials or rejected subscription documents.
	return &mongoOperationError{operation: operation, cause: err}
}

const (
	schemaObject   = "object"
	schemaRequired = "required"
)

// snapshotValidator defines the required fields and BSON types for wrapped subscriptions.
// Ordered BSON keeps collection-option comparisons stable across starts.
func snapshotValidator() bson.D {
	return bson.D{{Key: "$jsonSchema", Value: bson.D{
		{Key: schemaBSONType, Value: schemaObject},
		{Key: schemaRequired, Value: bson.A{fieldID, fieldSnapshotID, fieldSubscriptionID, "resource"}},
		{Key: schemaProperties, Value: bson.D{
			{Key: fieldID, Value: bson.D{{Key: schemaBSONType, Value: "objectId"}}},
			{Key: fieldSnapshotID, Value: versionIDSchema()},
			{Key: fieldSubscriptionID, Value: bson.D{{Key: schemaBSONType, Value: schemaString}}},
			{Key: "resource", Value: bson.D{{Key: schemaBSONType, Value: schemaObject}}},
		}},
	}}}
}

// versionIDSchema requires a lowercase, 24-character ObjectID string in stored metadata.
func versionIDSchema() bson.D {
	return bson.D{{Key: schemaBSONType, Value: schemaString}, {Key: "pattern", Value: "^[0-9a-f]{24}$"}}
}

// descriptorProperties defines the BSON types and basic bounds shared by head and history descriptors.
func descriptorProperties() bson.D {
	return bson.D{
		{Key: fieldSnapshotID, Value: versionIDSchema()},
		{Key: fieldSourceHash, Value: bson.D{{Key: schemaBSONType, Value: schemaString}, {Key: "pattern", Value: "^[0-9a-f]{64}$"}}},
		{Key: fieldDocumentCount, Value: bson.D{{Key: schemaBSONType, Value: "long"}, {Key: "minimum", Value: int64(0)}}},
		{Key: fieldCreatedAt, Value: bson.D{{Key: schemaBSONType, Value: "date"}}},
	}
}

// headValidator allows either an empty bootstrap head or active metadata with bounded, non-empty history.
func headValidator() bson.D {
	required := bson.A{fieldSnapshotID, fieldSourceHash, fieldDocumentCount, fieldCreatedAt}
	id := bson.D{{Key: schemaBSONType, Value: schemaString}, {Key: "enum", Value: bson.A{headID}}}
	properties := append(descriptorProperties(),
		bson.E{Key: fieldID, Value: id},
		bson.E{Key: fieldRecentSnapshots, Value: bson.D{
			{Key: schemaBSONType, Value: "array"},
			{Key: "minItems", Value: 1},
			{Key: "maxItems", Value: config.MaxRetainedSnapshots},
			{Key: "items", Value: bson.D{
				{
					Key:   schemaBSONType,
					Value: schemaObject,
				},
				{Key: schemaRequired, Value: required},
				{Key: schemaProperties, Value: descriptorProperties()},
			}},
		}},
	)
	bootstrap := bson.D{
		{Key: schemaRequired, Value: bson.A{fieldID, fieldRecentSnapshots}},
		{Key: "additionalProperties", Value: false},
		{Key: schemaProperties, Value: bson.D{
			{Key: fieldID, Value: id},
			{Key: fieldRecentSnapshots, Value: bson.D{{Key: schemaBSONType, Value: "array"}, {Key: "maxItems", Value: 0}}},
		}},
	}
	active := bson.D{
		{Key: schemaRequired, Value: append(bson.A{fieldID, fieldRecentSnapshots}, required...)},
		{Key: schemaProperties, Value: properties},
	}
	return bson.D{{Key: "$jsonSchema", Value: bson.D{
		{Key: schemaBSONType, Value: schemaObject}, {Key: "oneOf", Value: bson.A{bootstrap, active}},
	}}}
}

// setup checks the source, creates compatible output collections and installs indexes without TTL expiry.
func (m *mongoStore) setup(ctx context.Context) error {
	if err := m.requireSourceCollection(ctx); err != nil {
		return err
	}
	for _, target := range []struct {
		collection *mongo.Collection
		validator  bson.D
	}{
		{m.snapshots, snapshotValidator()},
		{m.heads, headValidator()},
	} {
		if err := m.ensureCollection(ctx, target.collection.Name(), target.validator); err != nil {
			return err
		}
		if err := rejectTTLIndexes(ctx, target.collection); err != nil {
			return err
		}
	}
	_, err := m.snapshots.Indexes().CreateMany(ctx, []mongo.IndexModel{
		{
			Keys: bson.D{{Key: fieldSnapshotID, Value: 1}, {Key: fieldSubscriptionID, Value: 1}},
			Options: options.Index().SetUnique(true).SetName("snapshot_subscription").
				SetCollation(&options.Collation{Locale: simpleCollation}),
		},
		{
			Keys: bson.D{
				{Key: fieldSnapshotID, Value: 1},
				{Key: "resource.spec.environment", Value: 1},
				{Key: "resource.spec.subscription.type", Value: 1},
			},
			Options: options.Index().SetName("snapshot_environment_type").SetCollation(&options.Collation{Locale: simpleCollation}),
		},
	})
	return databaseError("create snapshot indexes", err)
}

// requireSourceCollection rejects missing sources, views and capped collections before a source scan.
func (m *mongoStore) requireSourceCollection(ctx context.Context) error {
	cursor, err := m.database.ListCollections(ctx, bson.D{{Key: "name", Value: m.source.Name()}},
		options.ListCollections().SetNameOnly(true).SetAuthorizedCollections(true))
	if err != nil {
		return databaseError("check source collection", err)
	}
	var collections []struct {
		Type string `bson:"type"`
	}
	if err := cursor.All(ctx, &collections); err != nil {
		return databaseError("read source collection name and type", err)
	}
	if len(collections) != 1 {
		return errors.New("required subscription snapshot collection is missing or inaccessible")
	}
	if collections[0].Type != "collection" {
		return errors.New("subscription snapshots require an ordinary, writable, uncapped source collection")
	}

	// Collection-scoped collStats retains the capped check without reading database-wide metadata.
	var stats struct {
		Capped bool `bson:"capped"`
	}
	if err := m.database.RunCommand(ctx, bson.D{{Key: "collStats", Value: m.source.Name()}}).Decode(&stats); err != nil {
		return databaseError("check source collection storage", err)
	}
	if stats.Capped {
		return errors.New("subscription snapshots require an ordinary, writable, uncapped source collection")
	}
	return nil
}

// ensureCollection creates strict schema validation and rejects incompatible existing collection options.
func (m *mongoStore) ensureCollection(ctx context.Context, name string, validator bson.D) error {
	err := m.database.CreateCollection(ctx, name, options.CreateCollection().
		SetValidator(validator).SetValidationLevel("strict").SetValidationAction("error").
		SetCollation(&options.Collation{Locale: simpleCollation}))
	var commandError mongo.CommandError
	// create is idempotent only when the existing collection options match.
	if errors.As(err, &commandError) && commandError.Code == 48 {
		return errors.New("existing snapshot/head collection options differ; manual migration required")
	}
	return databaseError("create snapshot/head collection", err)
}

// rejectTTLIndexes rejects automatic expiry that could remove snapshots still protected by readers.
func rejectTTLIndexes(ctx context.Context, collection *mongo.Collection) error {
	indexes, err := collection.Indexes().ListSpecifications(ctx)
	if err != nil {
		return databaseError("inspect snapshot/head indexes", err)
	}
	for _, index := range indexes {
		if index.ExpireAfterSeconds != nil {
			return errors.New("snapshot/head TTL indexes would violate protected history; remove them manually")
		}
	}
	return nil
}
