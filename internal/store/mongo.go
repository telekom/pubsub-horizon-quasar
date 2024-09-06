// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type MongoStore struct {
	client *mongo.Client
}

func (m *MongoStore) Initialize() {
	var err error
	m.client, err = mongo.Connect(context.Background(), options.Client().ApplyURI(config.Current.Store.Mongo.Uri))
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create mongo-store")
	}

	if err := m.client.Ping(context.Background(), nil); err != nil {
		log.Fatal().Err(err).Msg("Could not reach mongodb")
	}

}

func (m *MongoStore) InitializeResource(resourceConfig *config.ResourceConfiguration) {
	for _, index := range resourceConfig.MongoIndexes {
		var model = index.ToIndexModel()
		var collection = m.client.Database(config.Current.Fallback.Mongo.Database).Collection(resourceConfig.GetCacheName())
		_, err := collection.Indexes().CreateOne(context.Background(), model)
		if err != nil {
			var resource = resourceConfig.GetGroupVersionResource()
			log.Warn().Fields(utils.CreateFieldForResource(&resource)).Err(err).Msg("Could not create index in MongoDB")
		}
	}
}

func (m *MongoStore) OnAdd(obj *unstructured.Unstructured) {
	json, err := obj.MarshalJSON()
	if err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not marshal resource to json string!")
	}

	var filter = bson.M{"_id": string(obj.GetUID())}
	var collection = m.getCollection(obj)

	_, err = collection.ReplaceOne(context.Background(), filter, json, options.Replace().SetUpsert(true))
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not add object to MongoDB")
	}

	log.Debug().Fields(utils.CreateFieldsForOp("add", obj)).Msg("Object added to MongoDB")
}

func (m *MongoStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) {
	json, err := newObj.MarshalJSON()
	if err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(newObj)).Err(err).Msg("Could not marshal resource to json string!")
	}

	var filter = bson.M{"_id": string(oldObj.GetUID())}
	var collection = m.getCollection(oldObj)

	_, err = collection.ReplaceOne(context.Background(), filter, json, options.Replace().SetUpsert(true))
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": newObj.GetUID(),
		}).Err(err).Msg("Could not add object to MongoDB")
	}

	log.Debug().Fields(utils.CreateFieldsForOp("add", newObj)).Msg("Object updated in MongoDB")
}

func (m *MongoStore) OnDelete(obj *unstructured.Unstructured) {
	var filter = bson.M{"_id": string(obj.GetUID())}

	_, err := m.getCollection(obj).DeleteOne(context.Background(), filter)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not delete object from MongoDB")
		return
	}
}

func (m *MongoStore) getCollection(obj *unstructured.Unstructured) *mongo.Collection {
	return m.client.Database(config.Current.Store.Mongo.Database).Collection(utils.GetGroupVersionId(obj))
}

func (m *MongoStore) Shutdown() {
	_ = m.client.Disconnect(context.TODO())
}
