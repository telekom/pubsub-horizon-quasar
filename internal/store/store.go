package store

import (
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

var CurrentStore = setupStore()

type Store interface {
	Initialize()
	OnAdd(obj *unstructured.Unstructured)
	OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured)
	OnDelete(obj *unstructured.Unstructured)
}

func setupStore() Store {
	var storeType = config.Current.StoreType
	store, err := createStore(storeType)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"storageType": storeType,
		}).Err(err).Msg("Could not create store!")
	}
	store.Initialize()
	return store
}

func createStore(storeType string) (Store, error) {
	switch strings.ToLower(storeType) {

	case "redis":
		return new(RedisStore), nil

	case "dummy":
		return new(DummyStore), nil

	default:
		return nil, ErrUnknownStoreType

	}
}
