// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"strings"
)

var CurrentStore Store

type Store interface {
	Initialize()
	InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.ResourceConfiguration)
	OnAdd(obj *unstructured.Unstructured)
	OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured)
	OnDelete(obj *unstructured.Unstructured)
	Count(mapName string) (int, error)
	Keys(mapName string) ([]string, error)
	Shutdown()
}

func SetupStore() {
	var storeType = config.Current.Store.StoreType
	var err error
	CurrentStore, err = createStore(storeType)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"storageType": storeType,
		}).Err(err).Msg("Could not create store!")
	}
	CurrentStore.Initialize()
}

func createStore(storeType string) (Store, error) {
	switch strings.ToLower(storeType) {

	case "redis":
		return new(RedisStore), nil

	case "hazelcast":
		return new(HazelcastStore), nil

	default:
		return nil, ErrUnknownStoreType

	}
}
