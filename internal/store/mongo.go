// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/reconciliation"
	"github.com/telekom/quasar/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type MongoStore struct {
	client    *mongo.Client
	ctx       context.Context
	connected atomic.Bool
}

func (m *MongoStore) Initialize() {
	var err error
	m.ctx = context.Background()
	m.client, err = mongo.Connect(m.ctx, options.Client().ApplyURI(config.Current.Store.Mongo.Uri))
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create mongo-store")
		m.connected.Store(false)
		return
	}

	if err := m.client.Ping(m.ctx, nil); err != nil {
		log.Fatal().Err(err).Msg("Could not reach mongodb")
		m.connected.Store(false)
		return
	}

	m.connected.Store(true)
	log.Info().Msg("MongoDB connection established")
}

func (m *MongoStore) InitializeResource(dataSource reconciliation.DataSource, resourceConfig *config.Resource) {
	_ = dataSource
	for _, index := range resourceConfig.MongoIndexes {
		model := index.ToIndexModel()
		collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(resourceConfig.GetGroupVersionName())
		_, err := collection.Indexes().CreateOne(m.ctx, model)
		if err != nil {
			resource := resourceConfig.GetGroupVersionResource()
			log.Warn().Fields(utils.CreateFieldForResource(&resource)).Err(err).Msg("Could not create index in MongoDB")
		}
	}
}

func (m *MongoStore) Create(obj *unstructured.Unstructured) error {
	collectionName := utils.GetGroupVersionId(obj)

	filter, err := m.createFilter(obj)
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "create", obj)).
			Msg("Failed to create or update document in MongoDB")
		return err
	}

	opts := options.Replace().SetUpsert(true)
	_, err = m.getCollection(obj).ReplaceOne(m.ctx, filter, obj.Object, opts)
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "create", obj)).
			Msg("Failed to create or update document in MongoDB")
		return err
	}

	log.Debug().
		Fields(utils.CreateFieldsForCollection(collectionName, "create", obj)).
		Msg("Resource created or updated in MongoDB")
	return nil
}

func (m *MongoStore) Update(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error {
	collectionName := utils.GetGroupVersionId(oldObj)

	filter, err := m.createFilter(oldObj)
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "update", oldObj)).
			Msg("Failed to update document in MongoDB")
		return err
	}

	opts := options.Replace().SetUpsert(true)
	_, err = m.getCollection(oldObj).ReplaceOne(m.ctx, filter, newObj.Object, opts)
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "update", oldObj)).
			Msg("Failed to update document in MongoDB")
		return err
	}

	log.Debug().
		Fields(utils.CreateFieldsForCollection(collectionName, "update", oldObj)).
		Msg("Resource updated in MongoDB")
	return nil
}

func (m *MongoStore) Delete(obj *unstructured.Unstructured) error {
	collectionName := utils.GetGroupVersionId(obj)

	filter, err := m.createFilter(obj)
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "delete", obj)).
			Msg("Failed to delete document in MongoDB")
		return err
	}

	_, err = m.getCollection(obj).DeleteOne(m.ctx, filter)
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "delete", obj)).
			Msg("Failed to delete document in MongoDB")
		return err
	}

	log.Debug().
		Fields(utils.CreateFieldsForCollection(collectionName, "delete", obj)).
		Msg("Resource deleted in MongoDB")
	return nil
}

func (m *MongoStore) Count(collectionName string) (int, error) {
	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(collectionName)

	count, err := collection.CountDocuments(m.ctx, bson.M{})
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "count", nil)).
			Msg("Failed to count documents in MongoDB")
		return 0, err
	}

	log.Debug().
		Fields(utils.CreateFieldsForCollection(collectionName, "count", nil)).
		Msg("Count documents in MongoDB")

	return int(count), nil
}

func (m *MongoStore) Keys(collectionName string) ([]string, error) {
	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(collectionName)

	keys, err := collection.Distinct(m.ctx, "_id", bson.M{})
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "keys", nil)).
			Msg("Failed to get distinct keys from MongoDB")
		return nil, err
	}

	// Convert interface{} slice to string slice
	stringKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		if strKey, ok := key.(string); ok {
			stringKeys = append(stringKeys, strKey)
		} else {
			stringKeys = append(stringKeys, fmt.Sprintf("%v", key))
		}
	}
	log.Debug().
		Fields(utils.CreateFieldsForCollection(collectionName, "keys", nil)).
		Msg("Keys retrieved from MongoDB")

	return stringKeys, nil
}

func (m *MongoStore) Read(collectionName string, key string) (*unstructured.Unstructured, error) {
	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(collectionName)

	filter := bson.M{"_id": key}
	var result unstructured.Unstructured

	err := collection.FindOne(m.ctx, filter).Decode(&result.Object)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollection(collectionName, "read", nil)).
			Str("key", key).
			Msg("Failed to read resource from MongoDB")
		return nil, err
	}

	log.Debug().
		Fields(utils.CreateFieldsForCollection(collectionName, "read", nil)).
		Str("key", key).
		Msg("Resource retrieved from MongoDB")

	return &result, nil
}

func (m *MongoStore) List(collectionName string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error) {
	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(collectionName)
	filter := bson.M{}

	// Apply field selector filtering if provided
	if fieldSelector != "" {
		fieldFilter, err := m.parseFieldSelector(fieldSelector)
		if err != nil {
			log.Warn().Err(err).
				Fields(utils.CreateFieldsForCollectionWithListOptions(collectionName, "list", nil, limit, fieldSelector)).
				Msg("Failed to parse field selector, ignoring")
		} else {
			for k, v := range fieldFilter {
				filter[k] = v
			}
		}
	}

	// Set find options
	findOptions := options.Find()
	if limit > 0 {
		findOptions.SetLimit(limit)
	}

	cursor, err := collection.Find(m.ctx, filter, findOptions)
	if err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollectionWithListOptions(collectionName, "list", nil, limit, fieldSelector)).
			Msg("Failed to list resources from MongoDB")
		return nil, err
	}
	defer func(cursor *mongo.Cursor, ctx context.Context) {
		if err := cursor.Close(ctx); err != nil {
			return
		}
	}(cursor, m.ctx)

	var results []unstructured.Unstructured
	for cursor.Next(m.ctx) {
		var resource unstructured.Unstructured
		if err := cursor.Decode(&resource.Object); err != nil {
			log.Error().Err(err).
				Fields(utils.CreateFieldsForCollectionWithListOptions(collectionName, "list", nil, limit, fieldSelector)).
				Msg("Failed to decode resource from MongoDB")
			continue
		}
		results = append(results, resource)
	}

	if err := cursor.Err(); err != nil {
		log.Error().Err(err).
			Fields(utils.CreateFieldsForCollectionWithListOptions(collectionName, "list", nil, limit, fieldSelector)).
			Msg("Cursor error while listing resources from MongoDB")
		return nil, err
	}

	log.Debug().
		Fields(utils.CreateFieldsForCollectionWithListOptions(collectionName, "list", nil, limit, fieldSelector)).
		Int("count", len(results)).
		Msg("Resources listed from MongoDB")

	return results, nil
}

func (m *MongoStore) Shutdown() {
	if m.Connected() {
		if err := m.client.Disconnect(m.ctx); err != nil {
			log.Error().Err(err).Msg("Could not disconnect from MongoDB")
		}
	}
	m.connected.Store(false)
}

func (m *MongoStore) Connected() bool {
	return m.connected.Load()
}

func (m *MongoStore) getCollection(obj *unstructured.Unstructured) *mongo.Collection {
	return m.client.Database(config.Current.Store.Mongo.Database).Collection(utils.GetGroupVersionId(obj))
}

func (m *MongoStore) createFilter(obj *unstructured.Unstructured) (bson.M, error) {
	id, err := utils.GetMongoId(obj)
	if err != nil {
		return nil, err
	}
	return bson.M{"_id": id}, nil
}

// Simple field selector parsing - supports key=value format
func (m *MongoStore) parseFieldSelector(fieldSelector string) (bson.M, error) {
	filter := bson.M{}

	if fieldSelector == "" {
		return filter, nil
	}

	// Split by comma for multiple selectors
	selectors := strings.Split(fieldSelector, ",")
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if strings.Contains(selector, "=") {
			parts := strings.SplitN(selector, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				filter[key] = value
			}
		}
	}
	return filter, nil
}
