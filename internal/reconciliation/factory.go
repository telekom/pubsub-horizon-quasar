// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/store"
	"k8s.io/client-go/dynamic"
)

// NewReconciliationForMode creates a reconciliation instance appropriate for the current mode
func NewReconciliationForMode(kubernetesClient dynamic.Interface, primaryStore store.Store, resource *config.Resource) *Reconciliation {
	var dataSource DataSource

	switch config.Current.Mode {
	case config.ModeWatcher:
		// In watcher mode, Kubernetes is the source of truth
		dataSource = NewKubernetesDataSource(kubernetesClient, resource)
	case config.ModeProvisioning:
		// In provisioning mode, the primary store (MongoDB) is the source of truth
		dataSource = NewStoreDataSource(primaryStore, resource)
	default:
		// Default to Kubernetes for backward compatibility
		dataSource = NewKubernetesDataSource(kubernetesClient, resource)
	}

	return NewReconciliation(dataSource, resource)
}
