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

func generateLogAttributes(operation string, id string, gvr schema.GroupVersionResource) map[string]any {
	result := make(map[string]any)

	if operation != "" {
		result["operation"] = operation
	}

	if id != "" {
		result["id"] = id
	}

	if gvr.Group != "" && gvr.Version != "" && gvr.Resource != "" {
		result["group"] = gvr.Group
		result["version"] = gvr.Version
		result["resource"] = gvr.Resource
	}
	return result
}

func getGvrFromContext(ctx *fiber.Ctx) (schema.GroupVersionResource, error) {
	gvr, ok := ctx.Locals("gvr").(schema.GroupVersionResource)
	if !ok || gvr.Version == "" || gvr.Resource == "" || gvr.Group == "" {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve group, version and resource from context")

		return schema.GroupVersionResource{}, &fiber.Error{
			Code:    fiber.StatusInternalServerError,
			Message: "Invalid or missing GVR in context",
		}
	}
	return gvr, nil
}

func getResourceIdFromContext(ctx *fiber.Ctx) (string, error) {
	name, ok := ctx.Locals("resourceId").(string)
	if !ok || name == "" {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve Resource Id from context")

		return "", &fiber.Error{
			Code:    fiber.StatusInternalServerError,
			Message: "Invalid or missing resource id in context",
		}
	}
	return name, nil
}

func getResourceFromContext(ctx *fiber.Ctx) (unstructured.Unstructured, error) {
	resource, ok := ctx.Locals("resource").(unstructured.Unstructured)
	if !ok {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve resource from context")

		return unstructured.Unstructured{}, &fiber.Error{
			Code:    fiber.StatusInternalServerError,
			Message: "invalid or missing resource in context",
		}
	}
	return resource, nil
}

// getGvrAndIdAndResourceFromContext performs all context validation for operations requiring GVR, ID, and Resource
func getGvrAndIdAndResourceFromContext(ctx *fiber.Ctx) (schema.GroupVersionResource, string, unstructured.Unstructured, error) {
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
	if err := validateResourceId(id, resource); err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	// Validate resource GVR matches URL
	if err := validateResourceApiVersion(gvr, resource); err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	// Validate resource kind correlates to URL resource
	if err := validateResourceKind(gvr, resource); err != nil {
		return schema.GroupVersionResource{}, "", unstructured.Unstructured{}, err
	}

	return gvr, id, resource, nil
}

// getGvrAndIdFromContext performs validation for operations requiring GVR and ID
func getGvrAndIdFromContext(ctx *fiber.Ctx) (schema.GroupVersionResource, string, error) {
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

func getDataSetForGvr(gvr schema.GroupVersionResource) string {
	for i, r := range config.Current.Resources {
		k := r.Kubernetes
		if k.Group == gvr.Group && k.Version == gvr.Version && k.Resource == gvr.Resource {
			return config.Current.Resources[i].GetGroupVersionName()
		}
	}
	logger.Warn().
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("No Kubernetes configuration found for gvr")
	return ""
}
