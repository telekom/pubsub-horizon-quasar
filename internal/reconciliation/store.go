// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// StoreDataSource implements DataSource using a store (e.g., MongoDB)
type StoreDataSource struct {
	store Store
}

// NewStoreDataSource creates a new store-based data source
func NewStoreDataSource(store Store) *StoreDataSource {
	return &StoreDataSource{
		store: store,
	}
}

// ListResources retrieves all resources from the store
func (s *StoreDataSource) ListResources(resourceName string) ([]unstructured.Unstructured, error) {
	return s.store.List(resourceName, "", 0)
}
