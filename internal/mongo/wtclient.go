// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package mongo

import (
	"context"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sync"
)

type WriteThroughClient struct {
	client *mongo.Client
	config *config.MongoConfiguration
	ctx    context.Context
	mutex  sync.Mutex
}

func NewWriteTroughClient(config *config.MongoConfiguration) *WriteThroughClient {
	var client, err = mongo.Connect(context.Background(), options.Client().ApplyURI(config.Uri))
	if err != nil {
		log.Fatal().Err(err).Msg("Could not connect to MongoDB")
	}

	if err := client.Ping(context.Background(), nil); err != nil {
		log.Fatal().Err(err).Msg("Could not reach MongoDB")
	}

	return &WriteThroughClient{
		client: client,
		config: config,
		ctx:    context.Background(),
	}
}

func (c *WriteThroughClient) Add(obj *unstructured.Unstructured) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var opts = options.Replace().SetUpsert(true)
	filter, err := c.createFilter(obj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "wt-add").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(obj.GetUID()))
		return
	}

	_, err = c.getCollection(obj).ReplaceOne(c.ctx, filter, obj.Object, opts)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not add object to MongoDB")
		return
	}

	log.Debug().Fields(utils.CreateFieldsForOp("wt-add", obj)).Msg("Object added to MongoDB")
}

func (c *WriteThroughClient) Update(obj *unstructured.Unstructured) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	var opts = options.Replace().SetUpsert(false)
	filter, err := c.createFilter(obj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "wt-update").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(obj.GetUID()))
		return
	}

	_, err = c.getCollection(obj).ReplaceOne(c.ctx, filter, obj.Object, opts)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not update object in MongoDB")
		return
	}

	log.Debug().Fields(utils.CreateFieldsForOp("wt-update", obj)).Msg("Object updated in MongoDB")
}

func (c *WriteThroughClient) Delete(obj *unstructured.Unstructured) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	filter, _, err := c.createFilterAndUpdate(obj)
	if err != nil {
		log.Error().
			Err(err).
			Str("action", "wt-delete").
			Msgf("Could not determine id for kubernetes resource with uid '%s'", string(obj.GetUID()))
		return
	}

	_, err = c.getCollection(obj).DeleteOne(c.ctx, filter)
	if err != nil {
		log.Warn().Fields(map[string]any{
			"_id": obj.GetUID(),
		}).Err(err).Msg("Could not delete object from MongoDB")
		return
	}

	log.Debug().Fields(utils.CreateFieldsForOp("wt-delete", obj)).Msg("Object deleted from MongoDB")
}

func (c *WriteThroughClient) EnsureIndexesOfResource(resourceConfig *config.ResourceConfiguration) {
	for _, index := range resourceConfig.MongoIndexes {
		var model = index.ToIndexModel()
		var collection = c.client.Database(config.Current.Fallback.Mongo.Database).Collection(resourceConfig.GetCacheName())
		_, err := collection.Indexes().CreateOne(c.ctx, model)
		if err != nil {
			var resource = resourceConfig.GetGroupVersionResource()
			log.Warn().Fields(utils.CreateFieldForResource(&resource)).Err(err).Msg("Could not create index in MongoDB")
		}
	}
}

func (*WriteThroughClient) createFilterAndUpdate(obj *unstructured.Unstructured) (bson.M, bson.D, error) {
	var objCopy = obj.DeepCopy().Object
	id, err := utils.GetMongoId(obj)
	if err != nil {
		return bson.M{}, bson.D{}, err
	}

	objCopy["_id"] = id
	return bson.M{"_id": id}, bson.D{{"$set", objCopy}}, nil
}

func (*WriteThroughClient) createFilter(obj *unstructured.Unstructured) (bson.M, error) {
	id, err := utils.GetMongoId(obj)
	if err != nil {
		return nil, err
	}

	return bson.M{"_id": id}, nil
}

func (c *WriteThroughClient) getCollection(obj *unstructured.Unstructured) *mongo.Collection {
	return c.client.Database(c.config.Database).Collection(utils.GetGroupVersionId(obj))
}

func (c *WriteThroughClient) Disconnect() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if err := c.client.Disconnect(c.ctx); err != nil {
		log.Error().Err(err).Msg("Could not disconnect from MongoDB")
	}
}
