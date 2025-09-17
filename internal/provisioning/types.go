// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// ResourceRequest represents a request for a cluster-scoped resource
type ResourceRequest struct {
	GVR      schema.GroupVersionResource
	Name     string
	Resource *unstructured.Unstructured
}

// ResourceResponse represents the response for resource operations
type ResourceResponse struct {
	Resource *unstructured.Unstructured  `json:"resource,omitempty"`
	Items    []unstructured.Unstructured `json:"items,omitempty"`
	Count    int                         `json:"count,omitempty"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}

// ListOptions represents query parameters for list operations
type ListOptions struct {
	LabelSelector string
	FieldSelector string
	Limit         int64
	Continue      string
}

// GetResourceKey returns a unique key for the cluster-scoped resource
func (r *ResourceRequest) GetResourceKey() string {
	return r.Name
}
