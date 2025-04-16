// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"github.com/google/uuid"
	"github.com/hazelcast/hazelcast-go-client"
	"github.com/hazelcast/hazelcast-go-client/cluster"
	"github.com/hazelcast/hazelcast-go-client/serialization"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/metrics"
	"github.com/telekom/quasar/internal/mongo"
	reconciler "github.com/telekom/quasar/internal/reconciliation"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"os"
	"time"
)

type HazelcastStore struct {
	client   *hazelcast.Client
	wtClient *mongo.WriteThroughClient
	ctx      context.Context
}

func (s *HazelcastStore) Initialize() {
	var hazelcastConfig = hazelcast.NewConfig()
	var err error

	instanceName, ok := os.LookupEnv("POD_NAME")
	if !ok {
		instanceName = "quasar-" + uuid.New().String()
	}

	hazelcastConfig.Cluster.Name = config.Current.Store.Hazelcast.ClusterName
	hazelcastConfig.ClientName = instanceName
	hazelcastConfig.Cluster.Security.Credentials.Username = config.Current.Store.Hazelcast.Username
	hazelcastConfig.Cluster.Security.Credentials.Password = config.Current.Store.Hazelcast.Password
	hazelcastConfig.Cluster.Network.Addresses = config.Current.Store.Hazelcast.Addresses
	hazelcastConfig.Cluster.Unisocket = config.Current.Store.Hazelcast.Unisocket
	hazelcastConfig.Logger.CustomLogger = new(utils.HazelcastZerologLogger)

	s.ctx = context.Background()
	s.client, err = hazelcast.StartNewClientWithConfig(s.ctx, hazelcastConfig)
	if err != nil {
		log.Panic().Err(err).Msg("Could not create hazelcast client!")
	}

	if config.Current.Store.Hazelcast.WriteBehind {
		s.wtClient = mongo.NewWriteTroughClient(&config.Current.Fallback.Mongo)
	}
}

func (s *HazelcastStore) InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.ResourceConfiguration) {
	if s.wtClient != nil {
		s.wtClient.EnsureIndexesOfResource(resourceConfig)
	}

	var mapName = resourceConfig.GetCacheName()
	cacheMap, err := s.client.GetMap(s.ctx, mapName)
	if err != nil {
		log.Panic().Fields(map[string]any{
			"name": mapName,
		}).Msg("Could not find map")
	}

	for _, index := range resourceConfig.HazelcastIndexes {
		var hazelcastIndex = index.ToIndexConfig()
		if err := cacheMap.AddIndex(s.ctx, hazelcastIndex); err != nil {
			log.Panic().Fields(map[string]any{
				"indexName": hazelcastIndex.Name,
			}).Err(err).Msg("Could not create hazelcast index")
		}
	}

	var reconciliation = reconciler.NewReconciliation(kubernetesClient, resourceConfig)
	_, err = s.client.AddMembershipListener(func(event cluster.MembershipStateChanged) {
		if event.State == cluster.MembershipStateRemoved {
			reconciliation.Reconcile(s)
		}
	})

	if err != nil {
		log.Error().Err(err).Fields(map[string]any{
			"cache": resourceConfig.GetCacheName(),
		}).Msg("Could not register membership listener for reconciliation")
	}

	go s.collectMetrics(resourceConfig.GetCacheName())
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

func (s *HazelcastStore) Shutdown() {
	if err := s.client.Shutdown(s.ctx); err != nil {
		log.Error().Err(err).Msg("Could not shutdown hazelcast client")
	}

	if s.wtClient != nil {
		s.wtClient.Disconnect()
	}
}

func (s *HazelcastStore) Count(mapName string) (int, error) {
	hzMap, err := s.client.GetMap(context.Background(), mapName)
	if err != nil {
		return 0, err
	}

	size, err := hzMap.Size(context.Background())
	if err != nil {
		return 0, err
	}

	return size, err
}

func (s *HazelcastStore) Keys(mapName string) ([]string, error) {
	hzMap, err := s.client.GetMap(context.Background(), mapName)
	if err != nil {
		return nil, err
	}

	keySet, err := hzMap.GetKeySet(context.Background())
	if err != nil {
		return nil, err
	}

	var keys = make([]string, 0)
	for _, key := range keySet {
		keys = append(keys, key.(string))
	}

	return keys, nil
}

func (s *HazelcastStore) getMap(obj *unstructured.Unstructured) *hazelcast.Map {
	var mapName = utils.GetGroupVersionId(obj)

	cacheMap, err := s.client.GetMap(s.ctx, mapName)
	if err != nil {
		log.Panic().Fields(map[string]any{
			"name": mapName,
		}).Msg("Could not find map!")
	}

	return cacheMap
}

func (s *HazelcastStore) collectMetrics(resourceName string) {
	if err := recover(); err != nil {
		log.Error().Msgf("Recovered from %s during hazelcast metric collection", err)
		return
	}

	for {
		hzMap, err := s.client.GetMap(context.Background(), resourceName)
		if err != nil {
			log.Error().Err(err).Fields(map[string]any{
				"map": hzMap.Name(),
			}).Msg("Could not collect data")
		}

		size, err := hzMap.Size(context.Background())
		if err != nil {
			log.Error().Err(err).Fields(map[string]any{
				"map": hzMap.Name(),
			}).Msg("Could not retrieve size")

			time.Sleep(15 * time.Second)
			continue
		}

		metrics.GetOrCreateCustom(resourceName + "_hazelcast_count").WithLabelValues().Set(float64(size))
		time.Sleep(15 * time.Second)
	}
}
