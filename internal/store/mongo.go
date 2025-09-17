// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"fmt"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"strings"
	"sync/atomic"
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

func (m *MongoStore) InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.ResourceConfiguration) {
	for _, index := range resourceConfig.MongoIndexes {
		var model = index.ToIndexModel()
		var collection = m.client.Database(config.Current.Store.Mongo.Database).Collection(resourceConfig.GetCacheName())
		_, err := collection.Indexes().CreateOne(m.ctx, model)
		if err != nil {
			var resource = resourceConfig.GetGroupVersionResource()
			log.Warn().Fields(utils.CreateFieldForResource(&resource)).Err(err).Msg("Could not create index in MongoDB")
		}
	}
}

func (m *MongoStore) OnAdd(obj *unstructured.Unstructured) error {
	filter, err := m.createFilter(obj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "add").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(obj.GetUID()))
		return err
	}

	var opts = options.Replace().SetUpsert(true)
	_, err = m.getCollection(obj).ReplaceOne(m.ctx, filter, obj.Object, opts)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not add object to MongoDB")
		return err
	}

	log.Debug().Fields(utils.CreateFieldsForCollection(m.getCollection(obj).Name(), "add", obj)).Msg("Resource added to MongoDB")
	return nil
}

func (m *MongoStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error {
	filter, err := m.createFilter(oldObj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "update").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(oldObj.GetUID()))
		return err
	}

	var opts = options.Replace().SetUpsert(true)
	_, err = m.getCollection(oldObj).ReplaceOne(m.ctx, filter, newObj.Object, opts)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": newObj.GetUID(),
		}).Err(err).Msg("Could not update object in MongoDB")
		return err
	}

	log.Debug().Fields(utils.CreateFieldsForCollection(m.getCollection(oldObj).Name(), "update", newObj)).Msg("Resource updated in MongoDB")
	return nil
}

func (m *MongoStore) OnDelete(obj *unstructured.Unstructured) error {
	filter, err := m.createFilter(obj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "delete").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(obj.GetUID()))
		return err
	}

	_, err = m.getCollection(obj).DeleteOne(m.ctx, filter)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not delete object from MongoDB")
		return err
	}

	log.Debug().Fields(utils.CreateFieldsForCollection(m.getCollection(obj).Name(), "delete", obj)).Msg("Resource deleted in MongoDB")
	return nil
}

func (m *MongoStore) Count(mapName string) (int, error) {
	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(mapName)

	count, err := collection.CountDocuments(m.ctx, bson.M{})
	if err != nil {
		return 0, err
	}

	return int(count), nil
}

func (m *MongoStore) Keys(mapName string) ([]string, error) {
	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(mapName)

	keys, err := collection.Distinct(m.ctx, "_id", bson.M{})
	if err != nil {
		return nil, err
	}

	// Convert interface{} slice to string slice
	var stringKeys = make([]string, 0, len(keys))
	for _, key := range keys {
		if strKey, ok := key.(string); ok {
			stringKeys = append(stringKeys, strKey)
		} else {
			stringKeys = append(stringKeys, fmt.Sprintf("%v", key))
		}
	}

	return stringKeys, nil
}

func (m *MongoStore) Get(gvr string, name string) (*unstructured.Unstructured, error) {
	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(gvr)

	filter := bson.M{"_id": name}
	var result unstructured.Unstructured

	err := collection.FindOne(m.ctx, filter).Decode(&result.Object)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, nil
		}
		log.Error().Err(err).
			Str("gvr", gvr).
			Str("name", name).
			Msg("Failed to get resource from MongoDB")
		return nil, err
	}

	log.Debug().
		Str("gvr", gvr).
		Str("name", name).
		Msg("Resource retrieved from MongoDB")

	return &result, nil
}

func (m *MongoStore) List(gvr string, labelSelector string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error) {

	collection := m.client.Database(config.Current.Store.Mongo.Database).Collection(gvr)

	filter := bson.M{}
	// Apply field selector filtering if provided
	if fieldSelector != "" {
		fieldFilter, err := m.parseFieldSelector(fieldSelector)
		if err != nil {
			log.Warn().Err(err).
				Str("fieldSelector", fieldSelector).
				Msg("Failed to parse field selector, ignoring")
		} else {
			for k, v := range fieldFilter {
				filter[k] = v
			}
		}
	}

	// Apply label selector filtering if provided
	if labelSelector != "" {
		labelFilter, err := m.parseLabelSelector(labelSelector)
		if err != nil {
			log.Warn().Err(err).
				Str("labelSelector", labelSelector).
				Msg("Failed to parse label selector, ignoring")
		} else {
			for k, v := range labelFilter {
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
			Str("gvr", gvr).
			Msg("Failed to list resources from MongoDB")
		return nil, err
	}
	defer cursor.Close(m.ctx)

	var results []unstructured.Unstructured
	for cursor.Next(m.ctx) {
		var resource unstructured.Unstructured
		if err := cursor.Decode(&resource.Object); err != nil {
			log.Error().Err(err).Msg("Failed to decode resource from MongoDB")
			continue
		}
		results = append(results, resource)
	}

	if err := cursor.Err(); err != nil {
		log.Error().Err(err).Msg("Cursor error while listing resources from MongoDB")
		return nil, err
	}

	log.Debug().
		Str("gvr", gvr).
		Int("count", len(results)).
		Str("labelSelector", labelSelector).
		Str("fieldSelector", fieldSelector).
		Int64("limit", limit).
		Msg("Resources listed from MongoDB")

	return results, nil
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

func (m *MongoStore) Shutdown() {
	if err := m.client.Disconnect(m.ctx); err != nil {
		log.Error().Err(err).Msg("Could not disconnect from MongoDB")
	}
	m.connected.Store(false)
}

func (m *MongoStore) Connected() bool {
	return m.connected.Load()
}

func (m *MongoStore) parseFieldSelector(fieldSelector string) (bson.M, error) {
	filter := bson.M{}

	// Simple field selector parsing - supports key=value format
	// For more complex parsing, we'd need a proper Kubernetes field selector parser
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

				// Map Kubernetes field names to MongoDB document structure
				switch key {
				case "metadata.name":
					filter["metadata.name"] = value
				case "metadata.namespace":
					filter["metadata.namespace"] = value
				default:
					filter[key] = value
				}
			}
		}
	}

	return filter, nil
}

func (m *MongoStore) parseLabelSelector(labelSelector string) (bson.M, error) {
	filter := bson.M{}

	// Simple label selector parsing - supports key=value format
	// For more complex parsing, we'd need a proper Kubernetes label selector parser
	if labelSelector == "" {
		return filter, nil
	}

	// Split by comma for multiple selectors
	selectors := strings.Split(labelSelector, ",")
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if strings.Contains(selector, "=") {
			parts := strings.SplitN(selector, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])

				// Map to MongoDB document structure for labels
				filter[fmt.Sprintf("metadata.labels.%s", key)] = value
			}
		}
	}

	return filter, nil
}
