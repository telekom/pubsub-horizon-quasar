// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResourceResponse represents the response for resource operations
type ResourceResponse struct {
	Resource *unstructured.Unstructured  `json:"resource,omitempty"`
	Items    []unstructured.Unstructured `json:"items,omitempty"`
	Count    int                         `json:"count,omitempty"`
	Keys     []string                    `json:"keys,omitempty"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Code    int    `json:"code,omitempty"`
	Details string `json:"details,omitempty"`
}
