// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"github.com/gofiber/fiber/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
)

// validateResourceId validates that the URL parameter name matches the resource name in the body
func validateResourceId(ctx *fiber.Ctx, id string, resource unstructured.Unstructured) error {
	if id != resource.GetName() {
		return handleBadRequestError(ctx, "Resource name in URL does not match resource name in body")
	}
	return nil
}

// validateResourceGVR validates that the URL parameter GVR matches the resource GVR in the body
func validateResourceGVR(ctx *fiber.Ctx, gvr schema.GroupVersionResource, resource unstructured.Unstructured) error {
	if resource.GetAPIVersion() != gvr.GroupVersion().String() {
		return handleBadRequestError(ctx, "Resource GroupVersion in URL does not match GVR in body")
	}
	return nil
}

// validateResourceKind validates that the URL resource parameter correlates to the kind in the body
func validateResourceKind(ctx *fiber.Ctx, gvr schema.GroupVersionResource, resource unstructured.Unstructured) error {
	expectedResource := strings.ToLower(fmt.Sprintf("%ss", resource.GetKind()))
	if gvr.Resource != expectedResource {
		return handleBadRequestError(ctx, "Resource in URL does not correlate to kind in body")
	}
	return nil
}

// validateContextWithGvrAndIdAndResource performs all context validation for operations requiring GVR, ID, and Resource
func validateContextWithGvrAndIdAndResource(ctx *fiber.Ctx) (schema.GroupVersionResource, string, unstructured.Unstructured, error) {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	id, err := getResourceIdFromContext(ctx)
	if err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	resource, err := getResourceFromContext(ctx)
	if err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	// Validate resource name matches URL
	if err := validateResourceId(ctx, id, resource); err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	// Validate resource GVR matches URL
	if err := validateResourceGVR(ctx, gvr, resource); err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	// Validate resource kind correlates to URL resource
	if err := validateResourceKind(ctx, gvr, resource); err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	return gvr, id, resource, nil
}

// validateContextWithGvr performs validation for operations requiring only GVR
func validateContextWithGvr(ctx *fiber.Ctx) (schema.GroupVersionResource, error) {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}

	return gvr, nil
}

// validateContextWithGvrAndId performs validation for operations requiring GVR and ID
func validateContextWithGvrAndId(ctx *fiber.Ctx) (schema.GroupVersionResource, string, error) {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return schema.GroupVersionResource{}, "", err
	}

	id, err := getResourceIdFromContext(ctx)
	if err != nil {
		return schema.GroupVersionResource{}, "", err
	}

	return gvr, id, nil
}
