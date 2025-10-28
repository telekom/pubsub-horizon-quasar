// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"context"

	"github.com/telekom/quasar/internal/config"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// KubernetesDataSource implements DataSource using Kubernetes API
type KubernetesDataSource struct {
	client   dynamic.Interface
	resource *config.Resource
}

// NewKubernetesDataSource creates a new Kubernetes-based data source
func NewKubernetesDataSource(client dynamic.Interface, resource *config.Resource) *KubernetesDataSource {
	return &KubernetesDataSource{
		client:   client,
		resource: resource,
	}
}

// ListResources retrieves all resources from Kubernetes
func (k *KubernetesDataSource) ListResources(resourceName string) ([]unstructured.Unstructured, error) {
	resources, err := k.client.Resource(k.resource.GetGroupVersionResource()).
		Namespace(k.resource.Kubernetes.Namespace).
		List(context.Background(), v1.ListOptions{})

	if err != nil {
		return nil, err
	}

	return resources.Items, nil
}
