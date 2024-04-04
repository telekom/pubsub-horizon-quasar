package store

import (
	"context"
	"fmt"
	"github.com/hazelcast/hazelcast-go-client"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type HazelcastStore struct {
	client *hazelcast.Client
	ctx    context.Context
}

func (s *HazelcastStore) Initialize() {
	var hazelcastConfig = hazelcast.NewConfig()
	var err error

	hazelcastConfig.Cluster.Name = config.Current.Hazelcast.ClusterName
	hazelcastConfig.Logger.CustomLogger = new(utils.HazelcastZerologLogger)

	s.ctx = context.Background()
	s.client, err = hazelcast.StartNewClientWithConfig(s.ctx, hazelcastConfig)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create hazelcast client!")
	}
}

func (s *HazelcastStore) OnAdd(obj *unstructured.Unstructured) {
	var cacheMap = s.getMap(obj)

	json, err := obj.MarshalJSON()
	if err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not marshal resource to json string!")
	}

	if err := cacheMap.Set(s.ctx, obj.GetName(), string(json)); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not write resource to store!")
	}
}

func (s *HazelcastStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) {
	var cacheMap = s.getMap(oldObj)

	json, err := newObj.MarshalJSON()
	if err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(newObj)).Err(err).Msg("Could not marshal resource to json string!")
	}

	if err := cacheMap.Set(s.ctx, newObj.GetName(), string(json)); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(newObj)).Err(err).Msg("Could not update resource in store!")
	}
}

func (s *HazelcastStore) OnDelete(obj *unstructured.Unstructured) {
	var cacheMap = s.getMap(obj)

	if err := cacheMap.Delete(s.ctx, obj.GetName()); err != nil {
		log.Error().Fields(utils.GetFieldsOfObject(obj)).Err(err).Msg("Could not delete resource from store!")
	}
}

func (*HazelcastStore) createMapName(obj *unstructured.Unstructured) string {
	var gvk = obj.GroupVersionKind()
	return fmt.Sprintf("%s.%s.%s", gvk.Kind, gvk.Group, gvk.Version)
}

func (s *HazelcastStore) getMap(obj *unstructured.Unstructured) *hazelcast.Map {
	var mapName = s.createMapName(obj)

	cacheMap, err := s.client.GetMap(s.ctx, mapName)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"name": mapName,
		}).Msg("Could not find map!")
	}

	return cacheMap
}
