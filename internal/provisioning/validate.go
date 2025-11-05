// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"github.com/gofiber/fiber/v2"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// validateResourceId validates that the URL parameter name matches the resource name in the body
func validateResourceId(id string, resource unstructured.Unstructured) error {
	if id != resource.GetName() {
		return &fiber.Error{
			Code:    fiber.StatusBadRequest,
			Message: "Resource name in URL does not match resource name in body",
		}
	}
	return nil
}

// validateResourceApiVersion validates that the URL parameter GVR matches the resource GVR in the body
func validateResourceApiVersion(gvr schema.GroupVersionResource, resource unstructured.Unstructured) error {
	if resource.GetAPIVersion() != gvr.GroupVersion().String() {
		return &fiber.Error{
			Code:    fiber.StatusBadRequest,
			Message: "Resource GroupVersion in URL does not match ApiVersion in body",
		}
	}
	return nil
}

// validateResourceKind validates that the URL resource parameter correlates to the kind in the body
func validateResourceKind(gvr schema.GroupVersionResource, resource unstructured.Unstructured) error {
	for i, r := range config.Current.Resources {
		k := r.Kubernetes
		if k.Group == gvr.Group && k.Version == gvr.Version && k.Resource == gvr.Resource {
			if resource.GetKind() == config.Current.Resources[i].Kubernetes.Kind {
				return nil
			}
			break
		}
	}

	return &fiber.Error{
		Code:    fiber.StatusBadRequest,
		Message: "Resource kind in body does not match configuration",
	}
}
