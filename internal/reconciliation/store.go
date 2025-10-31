// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// StoreDataSource implements reconciliation DataSource using a store (e.g., MongoDB)
type StoreDataSource struct {
	store    Store
	resource config.Resource
}

// NewDataSourceFromStore creates a new store-based data source
func NewDataSourceFromStore(store Store, resource config.Resource) *StoreDataSource {
	return &StoreDataSource{
		store:    store,
		resource: resource,
	}
}

// ListResources retrieves all resources from the store relevant for reconciliation
func (s *StoreDataSource) ListResources() ([]unstructured.Unstructured, error) {
	resources, err := s.store.List(s.resource.GetDataSet(), "", 0)
	if err != nil {
		return nil, err
	}
	return resources, nil
}
