// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"

	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	schemaObject   = "object"
	schemaRequired = "required"
)

// snapshotValidator uses ordered BSON to keep create's existing-options comparison stable across starts.
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

func versionIDSchema() bson.D {
	return bson.D{{Key: schemaBSONType, Value: schemaString}, {Key: "pattern", Value: "^[0-9a-f]{24}$"}}
}

func descriptorProperties() bson.D {
	return bson.D{
		{Key: fieldSnapshotID, Value: versionIDSchema()},
		{Key: fieldSourceHash, Value: bson.D{{Key: schemaBSONType, Value: schemaString}, {Key: "pattern", Value: "^[0-9a-f]{64}$"}}},
		{Key: fieldDocumentCount, Value: bson.D{{Key: schemaBSONType, Value: "long"}, {Key: "minimum", Value: int64(0)}}},
		{Key: fieldCreatedAt, Value: bson.D{{Key: schemaBSONType, Value: "date"}}},
	}
}

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
