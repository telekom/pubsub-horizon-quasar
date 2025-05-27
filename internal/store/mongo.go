// Copyright 2024 Deutsche Telekom AG
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
	"k8s.io/client-go/dynamic"
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

func (m *MongoStore) InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.ResourceConfiguration) {
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
	id, err := utils.GetMongoId(obj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "add").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(obj.GetUID()))
		return
	}

	var filter = bson.M{"_id": id}
	var collection = m.getCollection(obj)

	_, err = collection.ReplaceOne(context.Background(), filter, obj.Object, options.Replace().SetUpsert(true))
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not add object to MongoDB")
	}

	log.Debug().Fields(utils.CreateFieldsForOp("add", obj)).Msg("Object added to MongoDB")
}

func (m *MongoStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) {
	id, err := utils.GetMongoId(newObj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "update").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(oldObj.GetUID()))
		return
	}

	var filter = bson.M{"_id": id}
	var collection = m.getCollection(oldObj)

	_, err = collection.ReplaceOne(context.Background(), filter, newObj.Object, options.Replace().SetUpsert(true))
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": newObj.GetUID(),
		}).Err(err).Msg("Could not add object to MongoDB")
	}

	log.Debug().Fields(utils.CreateFieldsForOp("add", newObj)).Msg("Object updated in MongoDB")
}

func (m *MongoStore) OnDelete(obj *unstructured.Unstructured) {
	id, err := utils.GetMongoId(obj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "delete").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(obj.GetUID()))
		return
	}

	var filter = bson.M{"_id": id}

	_, err = m.getCollection(obj).DeleteOne(context.Background(), filter)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not delete object from MongoDB")
		return
	}
}

func (m *MongoStore) Count(mapName string) (int, error) {
	//TODO implement me
	panic("implement me")
}

func (m *MongoStore) Keys(mapName string) ([]string, error) {
	//TODO implement me
	panic("implement me")
}

func (m *MongoStore) getCollection(obj *unstructured.Unstructured) *mongo.Collection {
	return m.client.Database(config.Current.Store.Mongo.Database).Collection(utils.GetGroupVersionId(obj))
}

func (m *MongoStore) Shutdown() {
	_ = m.client.Disconnect(context.TODO())
}
func (s *MongoStore) Connected() bool { panic("implement me") }
