// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// StoreDataSource implements DataSource using a store (e.g., MongoDB)
type StoreDataSource struct {
	lister   ResourceLister
	resource *config.Resource
}

// NewStoreDataSource creates a new store-based data source
func NewStoreDataSource(lister ResourceLister, resource *config.Resource) *StoreDataSource {
	return &StoreDataSource{
		lister:   lister,
		resource: resource,
	}
}

// ListResources retrieves all resources from the store
func (s *StoreDataSource) ListResources(resourceName string) ([]unstructured.Unstructured, error) {
	// Use the store's List method to get all resources
	return s.lister.List(resourceName, "", 0)
}
