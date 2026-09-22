// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"reflect"

	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func snapshotValidator() bson.M {
	return bson.M{"$jsonSchema": bson.M{
		"bsonType": "object", "required": bson.A{"_id", "snapshotId", "subscriptionId", "resource"},
		"properties": bson.M{
			"_id":            bson.M{"bsonType": "objectId"},
			"snapshotId":     versionIDSchema(),
			"subscriptionId": bson.M{"bsonType": "string"},
			"resource":       bson.M{"bsonType": "object"},
		},
	}}
}

func versionIDSchema() bson.M {
	return bson.M{"bsonType": "string", "pattern": "^[0-9a-f]{24}$"}
}

func descriptorProperties() bson.M {
	return bson.M{
		"snapshotId":    versionIDSchema(),
		"sourceHash":    bson.M{"bsonType": "string", "pattern": "^[0-9a-f]{64}$"},
		"documentCount": bson.M{"bsonType": "long", "minimum": int64(0)},
		"createdAt":     bson.M{"bsonType": "date"},
	}
}

func headValidator() bson.M {
	required := bson.A{"snapshotId", "sourceHash", "documentCount", "createdAt"}
	properties := descriptorProperties()
	properties["_id"] = bson.M{"bsonType": "string", "enum": bson.A{"head"}}
	properties["recentSnapshots"] = bson.M{
		"bsonType": "array", "minItems": 1, "maxItems": config.MaxSnapshotHistory,
		"items": bson.M{"bsonType": "object", "required": required, "properties": descriptorProperties()},
	}
	bootstrap := bson.M{
		"required": bson.A{"_id", "recentSnapshots"}, "additionalProperties": false,
		"properties": bson.M{
			"_id":             properties["_id"],
			"recentSnapshots": bson.M{"bsonType": "array", "maxItems": 0},
		},
	}
	active := bson.M{
		"required": append(bson.A{"_id", "recentSnapshots"}, required...), "properties": properties,
	}
	return bson.M{"$jsonSchema": bson.M{"bsonType": "object", "oneOf": bson.A{bootstrap, active}}}
}

func (m *mongoStore) setup(ctx context.Context) error {
	if _, err := m.collectionSpec(ctx, m.source.Name()); err != nil {
		return err
	}
	for _, target := range []struct {
		collection *mongo.Collection
		validator  bson.M
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
			Keys: bson.D{{Key: "snapshotId", Value: 1}, {Key: "subscriptionId", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("snapshot_subscription").
				SetCollation(&options.Collation{Locale: "simple"}),
		},
		{
			Keys: bson.D{
				{Key: "snapshotId", Value: 1},
				{Key: "resource.spec.environment", Value: 1},
				{Key: "resource.spec.subscription.type", Value: 1},
			},
			Options: options.Index().SetName("snapshot_environment_type").SetCollation(&options.Collation{Locale: "simple"}),
		},
	})
	return databaseError("create snapshot indexes", err)
}

func (m *mongoStore) collectionSpec(ctx context.Context, name string) (*mongo.CollectionSpecification, error) {
	specs, err := m.database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil {
		return nil, databaseError("inspect collection", err)
	}
	if len(specs) != 1 {
		return nil, errors.New("required subscription snapshot collection is missing")
	}
	spec := specs[0]
	capped, _ := spec.Options.Lookup("capped").BooleanOK()
	if spec.Type != "collection" || spec.ReadOnly || spec.Options.Lookup("timeseries").Type != 0 ||
		capped {

		return nil, errors.New("subscription snapshots require ordinary, writable, uncapped collections")
	}
	return spec, nil
}

func (m *mongoStore) ensureCollection(ctx context.Context, name string, validator bson.M) error {
	err := m.database.CreateCollection(ctx, name, options.CreateCollection().
		SetValidator(validator).SetValidationLevel("strict").SetValidationAction("error").
		SetCollation(&options.Collation{Locale: "simple"}))
	var commandError mongo.CommandError
	if err != nil && (!errors.As(err, &commandError) || commandError.Code != 48) {
		return databaseError("create snapshot/head collection", err)
	}
	spec, err := m.collectionSpec(ctx, name)
	if err != nil {
		return err
	}
	if err := validateCollectionOptions(spec.Options); err != nil {
		return err
	}
	existing, ok := spec.Options.Lookup("validator").DocumentOK()
	if !ok || len(existing) == 5 {
		return databaseError("install snapshot/head validator", m.database.RunCommand(ctx, bson.D{
			{Key: "collMod", Value: name},
			{Key: "validator", Value: validator},
			{Key: "validationLevel", Value: "strict"},
			{Key: "validationAction", Value: "error"},
			{Key: "writeConcern", Value: m.writeConcern},
		}).Err())
	}
	var actual bson.M
	if err := bson.Unmarshal(existing, &actual); err != nil {
		return errors.New("cannot decode existing snapshot/head validator")
	}
	expectedBytes, err := bson.Marshal(validator)
	if err != nil {
		return errors.New("cannot encode snapshot/head validator")
	}
	var expected bson.M
	if err := bson.Unmarshal(expectedBytes, &expected); err != nil {
		return errors.New("cannot decode expected snapshot/head validator")
	}
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("existing snapshot/head validator differs; manual migration required")
	}
	return nil
}

func validateCollectionOptions(raw bson.Raw) error {
	for key, expected := range map[string]string{"validationLevel": "strict", "validationAction": "error"} {
		value, exists := raw.Lookup(key).StringValueOK()
		if exists && value != expected {
			return errors.New("existing snapshot/head validation settings require manual correction")
		}
	}
	if collation, ok := raw.Lookup("collation").DocumentOK(); ok {
		if locale, _ := collation.Lookup("locale").StringValueOK(); locale != "simple" {
			return errors.New("snapshot/head collections require simple collation")
		}
	}
	return nil
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
