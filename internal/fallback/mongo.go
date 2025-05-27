// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package fallback

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
)

type MongoFallback struct {
	client *mongo.Client
}

func (m *MongoFallback) Initialize() {
	var ctx = context.Background()

	var client, err = mongo.Connect(ctx, options.Client().ApplyURI(config.Current.Fallback.Mongo.Uri))
	if err != nil {
		log.Fatal().Err(err).Msg("Could not connect to MongoDB")
	}

	if err := client.Ping(context.Background(), nil); err != nil {
		log.Fatal().Err(err).Msg("Could not reach MongoDB")
	}
}

func (m *MongoFallback) ReplayResource(gvr *schema.GroupVersionResource, replayFunc ReplayFunc) (int64, error) {
	var ctx = context.Background()

	var col = m.getCollection(gvr)
	count, err := col.EstimatedDocumentCount(ctx)
	if err != nil {
		return 0, err
	}

	var fields = utils.CreateFieldForResource(gvr)
	fields["estDocumentCount"] = count
	log.Debug().Fields(fields).Msg("Starting replay of resource")

	cursor, err := col.Find(ctx, bson.D{})
	if err != nil {
		return 0, err
	}

	var replayedDocuments int64
	for cursor.Next(ctx) {
		var retrieved map[string]any
		if err := cursor.Decode(&retrieved); err != nil {
			log.Error().Err(err).Msg("Could not decode retrieved document")
			continue
		}

		bytes, _ := json.Marshal(retrieved)

		var unstructuredObj unstructured.Unstructured
		_ = unstructuredObj.UnmarshalJSON(bytes)

		replayFunc(&unstructuredObj)
		replayedDocuments++
		log.Debug().Fields(utils.CreateFieldsForOp("replay", &unstructuredObj)).Msg("Replayed resource from MongoDB")
	}

	return replayedDocuments, nil
}

func (m *MongoFallback) getCollection(gvr *schema.GroupVersionResource) *mongo.Collection {
	var collectionName = strings.ToLower(fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Group, gvr.Version))
	return m.client.Database(config.Current.Fallback.Mongo.Database).Collection(collectionName)
}
