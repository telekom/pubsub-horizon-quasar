// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"github.com/gofiber/fiber/v2"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strconv"
)

// putResource handles PUT requests to create or replace a Kubernetes resource                                                                │ │
// URL params: group, version, resource, name                                                                                                 │ │
// Request body: JSON Kubernetes resource (name/GVR must match URL)                                                                           │ │
// Response: HTTP 200 with empty body on success
func putResource(ctx *fiber.Ctx) error {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}

	name, err := getResourceNameFromContext(ctx)
	if err != nil {
		return err
	}

	resource, err := getResourceFromContext(ctx)
	if err != nil {
		return err
	}

	// Verify if url param name is present in resource
	if name != resource.GetName() {
		return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Resource name in URL does not match resource name in body",
			Code:  fiber.StatusBadRequest,
		})
	}
	// Verify if url param gvr is present in resource
	if resource.GetAPIVersion() != gvr.GroupVersion().String() {
		return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Resource GroupVersion in URL does not match GVR in body",
			Code:  fiber.StatusBadRequest,
		})
	}

	logger.Debug().
		Str("name", name).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Put-Request for resource")

	// Store resource
	if storeManager != nil {
		utils.AddMissingEnvironment(&resource)
		if err := storeManager.OnAdd(&resource); err != nil {
			logger.Error().
				Err(err).
				Str("name", name).
				Str("group", gvr.Group).
				Str("version", gvr.Version).
				Str("resource", gvr.Resource).
				Msg("Failed to put resource")

			return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Error:   "Failed to put resource",
				Code:    fiber.StatusInternalServerError,
				Details: err.Error(),
			})
		}
	}

	logger.Debug().
		Str("name", resource.GetName()).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Resource put successfully")

	return ctx.Status(fiber.StatusOK).Send(nil)
}

// getResource handles GET requests to retrieve a specific Kubernetes resource
// URL params: group, version, resource, name
// Response: HTTP 200 with resource JSON or HTTP 404 if not found
func getResource(ctx *fiber.Ctx) error {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}

	name, err := getResourceNameFromContext(ctx)
	if err != nil {
		return err
	}

	logger.Debug().
		Str("name", name).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Get-Request for resource")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	storeObject := gvr.Resource + "." + gvr.Group + "." + gvr.Version
	resource, err := storeManager.Get(storeObject, name)
	if err != nil {
		logger.Error().
			Err(err).
			Str("name", name).
			Msg("Failed to get resource")

		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error:   "Failed to get resource",
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
		})
	}

	if resource == nil {
		return ctx.Status(fiber.StatusNotFound).JSON(ErrorResponse{
			Error: "Resource not found",
			Code:  fiber.StatusNotFound,
		})
	}

	logger.Debug().
		Str("name", name).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Resource retrieved successfully")

	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Resource: resource,
	})
}

// listResources handles GET requests to list Kubernetes resources of a specific type
// URL params: group, version, resource
// Query params: labelSelector, fieldSelector, limit (default: 10000)
// Response: HTTP 200 with array of resources
func listResources(ctx *fiber.Ctx) error {

	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}

	// Parse query parameters
	labelSelector := ctx.Query("labelSelector", "")
	fieldSelector := ctx.Query("fieldSelector", "")
	limitStr := ctx.Query("limit", "")

	limit, err := strconv.ParseInt(limitStr, 10, 64)
	if err != nil {
		limit = 10000
	}

	logger.Debug().
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Str("labelSelector", labelSelector).
		Str("fieldSelector", fieldSelector).
		Int64("limit", limit).
		Msg("List-Request for resources")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	storeObject := gvr.Resource + "." + gvr.Group + "." + gvr.Version
	resources, err := storeManager.List(storeObject, labelSelector, fieldSelector, limit)
	if err != nil {
		logger.Error().
			Err(err).
			Str("group", gvr.Group).
			Str("version", gvr.Version).
			Str("resource", gvr.Resource).
			Msg("Failed to list resources")

		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error:   "Failed to list resources",
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
		})
	}

	logger.Debug().
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Int("count", len(resources)).
		Msg("Resources listed successfully")

	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Items: resources,
		Count: len(resources),
	})
}

// deleteResource handles DELETE requests to remove a Kubernetes resource
// URL params: group, version, resource, name
// Request body: JSON Kubernetes resource (name/GVR must match URL)
// Response: HTTP 204 with empty body on success
func deleteResource(ctx *fiber.Ctx) error {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}

	name, err := getResourceNameFromContext(ctx)
	if err != nil {
		return err
	}

	resource, err := getResourceFromContext(ctx)
	if err != nil {
		return err
	}

	// Verify if url param gvr is present in resource
	if resource.GetAPIVersion() != gvr.GroupVersion().String() {
		return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Resource GroupVersion in URL does not match GVR in body",
			Code:  fiber.StatusBadRequest,
		})
	}

	// Verify if url param name is present in resource
	if name != resource.GetName() {
		return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Resource name in URL does not match resource name in body",
			Code:  fiber.StatusBadRequest,
		})
	}

	logger.Debug().
		Str("name", name).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Delete-Request for resource")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	if err := storeManager.OnDelete(&resource); err != nil {
		logger.Error().
			Err(err).
			Str("name", name).
			Msg("Failed to delete resource")

		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error:   "Failed to delete resource",
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
		})
	}

	logger.Debug().
		Str("name", name).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Resource deleted successfully")

	return ctx.Status(fiber.StatusNoContent).Send(nil)
}

func getGvrFromContext(ctx *fiber.Ctx) (schema.GroupVersionResource, error) {
	gvr, ok := ctx.Locals("gvr").(schema.GroupVersionResource)
	if !ok || gvr.Version == "" || gvr.Resource == "" || gvr.Group == "" {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve group, version and resource from context")

		return schema.GroupVersionResource{},
			ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Error: "Failed to retrieve group, version and resource from context",
				Code:  fiber.StatusInternalServerError,
			})
	}
	return gvr, nil
}

func getResourceNameFromContext(ctx *fiber.Ctx) (string, error) {
	name, ok := ctx.Locals("name").(string)
	if !ok || name == "" {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve name from context")

		return "",
			ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
				Error: "Failed to retrieve resource name from context",
				Code:  fiber.StatusInternalServerError,
			})
	}
	return name, nil
}

func getResourceFromContext(ctx *fiber.Ctx) (unstructured.Unstructured, error) {
	resource, ok := ctx.Locals("resource").(unstructured.Unstructured)
	if !ok {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve resource from context")

		return unstructured.Unstructured{}, ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: "Failed to retrieve resource from context",
			Code:  fiber.StatusInternalServerError,
		})
	}
	return resource, nil
}
