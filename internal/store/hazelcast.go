package store

import (
	"context"
	"fmt"
	"github.com/hazelcast/hazelcast-go-client"
	"github.com/hazelcast/hazelcast-go-client/serialization"
	"github.com/hazelcast/hazelcast-go-client/types"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/mongo"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
)

type HazelcastStore struct {
	client   *hazelcast.Client
	wtClient *mongo.WriteThroughClient
	ctx      context.Context
}

func (s *HazelcastStore) Initialize() {
	var hazelcastConfig = hazelcast.NewConfig()
	var err error

	hazelcastConfig.Cluster.Name = config.Current.Store.Hazelcast.ClusterName
	hazelcastConfig.Cluster.Security.Credentials.Username = config.Current.Store.Hazelcast.Username
	hazelcastConfig.Cluster.Security.Credentials.Password = config.Current.Store.Hazelcast.Password
	hazelcastConfig.Logger.CustomLogger = new(utils.HazelcastZerologLogger)

	s.ctx = context.Background()
	s.client, err = hazelcast.StartNewClientWithConfig(s.ctx, hazelcastConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create hazelcast client!")
	}
	s.ensureIndexes()

	if config.Current.Store.Hazelcast.Mongo.Enabled {
		s.wtClient = mongo.NewWriteTroughClient(&config.Current.Store.Hazelcast.Mongo)
		s.wtClient.EnsureIndexes()
	}
}

func (s *HazelcastStore) OnAdd(obj *unstructured.Unstructured) {
	var cacheMap = s.getMap(obj)

	json, err := obj.MarshalJSON()
	if err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not marshal resource to json string!")
	}

	if err := cacheMap.Set(s.ctx, obj.GetName(), serialization.JSON(json)); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not write resource to store!")
	}

	if s.wtClient != nil {
		go s.wtClient.Add(obj)
	}
}

func (s *HazelcastStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) {
	var cacheMap = s.getMap(oldObj)

	json, err := newObj.MarshalJSON()
	if err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(newObj)).Err(err).Msg("Could not marshal resource to json string!")
	}

	if err := cacheMap.Set(s.ctx, newObj.GetName(), serialization.JSON(json)); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(newObj)).Err(err).Msg("Could not update resource in store!")
	}

	if s.wtClient != nil {
		go s.wtClient.Update(newObj)
	}
}

func (s *HazelcastStore) OnDelete(obj *unstructured.Unstructured) {
	var cacheMap = s.getMap(obj)

	if err := cacheMap.Delete(s.ctx, obj.GetName()); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not delete resource from store!")
	}

	if s.wtClient != nil {
		go s.wtClient.Delete(obj)
	}
}

func (s *HazelcastStore) ensureIndexes() {
	var resource = config.Current.Kubernetes.GetGroupVersionResource()
	var cacheMap = s.getMapByGvr(&resource)

	for _, index := range config.Current.Store.Hazelcast.Indexes {
		err := cacheMap.AddIndex(s.ctx, types.IndexConfig{
			Name:       index.Name,
			Attributes: index.Fields,
			Type:       types.IndexTypeSorted,
		})
		if err != nil {
			log.Error().Err(err).Msg("Could not create hazelcast index!")
		}
	}
}

func (s *HazelcastStore) getMap(obj *unstructured.Unstructured) *hazelcast.Map {
	var mapName = utils.GetGroupVersionId(obj)

	cacheMap, err := s.client.GetMap(s.ctx, mapName)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"name": mapName,
		}).Msg("Could not find map!")
	}

	return cacheMap
}

func (s *HazelcastStore) getMapByGvr(schema *schema.GroupVersionResource) *hazelcast.Map {
	var mapName = strings.ToLower(fmt.Sprintf("%s.%s.%s", schema.Resource, schema.Group, schema.Version))

	cacheMap, err := s.client.GetMap(s.ctx, mapName)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"name": mapName,
		}).Msg("Could not find map!")
	}

	return cacheMap
}
