package store

import (
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

var CurrentStore Store

type Store interface {
	Initialize()
	InitializeResource(resourceConfig *config.ResourceConfiguration)
	OnAdd(obj *unstructured.Unstructured)
	OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured)
	OnDelete(obj *unstructured.Unstructured)
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

	case "dummy":
		return new(DummyStore), nil

	default:
		return nil, ErrUnknownStoreType

	}
}
