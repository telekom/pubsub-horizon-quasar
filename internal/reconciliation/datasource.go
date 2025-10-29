// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DataSource represents a source of truth for reconciliation
type DataSource interface {
	ListResources(resourceName string) ([]unstructured.Unstructured, error)
}

// ResourceLister provides minimal interface for listing resources from stores
// This interface is satisfied by any store that implements List and prevents circular dependencies
type ResourceLister interface {
	List(dataset string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error)
}
