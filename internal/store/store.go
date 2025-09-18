// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"strings"
)

type Store interface {
	Initialize()
	InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.ResourceConfiguration)
	Create(obj *unstructured.Unstructured) error
	Update(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error
	Delete(obj *unstructured.Unstructured) error
	Count(dataset string) (int, error)
	Keys(dataset string) ([]string, error)
	Read(dataset string, key string) (*unstructured.Unstructured, error)
	List(dataset string, labelSelector string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error)
	Shutdown()
	Connected() bool
}

func createStore(storeType string) (Store, error) {
	switch strings.ToLower(storeType) {

	case "redis":
		return new(RedisStore), nil

	case "hazelcast":
		return new(HazelcastStore), nil

	case "mongo":
		return new(MongoStore), nil

	default:
		return nil, ErrUnknownStoreType

	}
}
