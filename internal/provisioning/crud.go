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

// putResource handles PUT requests to create or replace cluster-scoped resources
// Url params for group, version, resource and name
// Responds with http-200 and empty response body
func putResource(ctx *fiber.Ctx) error {
	gvr := ctx.Locals("gvr").(schema.GroupVersionResource)
	name := ctx.Locals("name").(string)
	resource, ok := ctx.Locals("resource").(unstructured.Unstructured)
	if !ok {
		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: "Failed to retrieve resource from context",
			Code:  fiber.StatusInternalServerError,
		})
	}

	// Set name from URL if not present in resource
	if name != "" && resource.GetName() == "" {
		resource.SetName(name)
	}

	logger.Debug().
		Str("name", resource.GetName()).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Putting cluster-scoped resource")

	if storeManager != nil {
		utils.AddMissingEnvironment(&resource)
		if err := storeManager.OnAdd(&resource); err != nil {
			logger.Error().
				Err(err).
				Str("name", resource.GetName()).
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

// getResource handles GET requests
// Url params for group, version, resource and name
// Responds with http-200 and the resource in the body
func getResource(ctx *fiber.Ctx) error {
	req := ctx.Locals("resourceRequest").(*ResourceRequest)

	logger.Debug().
		Str("name", req.Name).
		Str("group", req.GVR.Group).
		Str("version", req.GVR.Version).
		Str("resource", req.GVR.Resource).
		Msg("Getting cluster-scoped resource")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	gvr := req.GVR.Resource + "/" + req.GVR.Group + "/" + req.GVR.Version
	resource, err := storeManager.Get(gvr, req.Name)
	if err != nil {
		logger.Error().
			Err(err).
			Str("name", req.Name).
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
		Str("name", req.Name).
		Str("group", req.GVR.Group).
		Str("version", req.GVR.Version).
		Str("resource", req.GVR.Resource).
		Msg("Resource retrieved successfully")

	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Resource: resource,
	})
}

// listResources handles GET requests for listing cluster-scoped resources
func listResources(ctx *fiber.Ctx) error {
	gvr := ctx.Locals("gvr").(schema.GroupVersionResource)

	// Parse query parameters
	labelSelector := ctx.Query("labelSelector", "")
	fieldSelector := ctx.Query("fieldSelector", "")
	limitStr := ctx.Query("limit", "100")
	_ = ctx.Query("continue", "")

	limit, err := strconv.ParseInt(limitStr, 10, 64)
	if err != nil {
		limit = 100
	}

	logger.Debug().
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Str("labelSelector", labelSelector).
		Str("fieldSelector", fieldSelector).
		Int64("limit", limit).
		Msg("Listing cluster-scoped resources")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	mapName := gvr.Resource + "." + gvr.Group + "." + gvr.Version
	resources, err := storeManager.List(mapName, labelSelector, fieldSelector, limit)
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

// deleteResource handles DELETE requests
// Url params for group, version, resource and name
// Responds with http-204 and empty response body
func deleteResource(ctx *fiber.Ctx) error {
	gvr := ctx.Locals("gvr").(schema.GroupVersionResource)
	name := ctx.Locals("name").(string)
	resource, ok := ctx.Locals("resource").(unstructured.Unstructured)

	if !ok {
		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error: "Failed to retrieve resource from context",
			Code:  fiber.StatusInternalServerError,
		})
	}

	logger.Debug().
		Str("name", name).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Deleting cluster-scoped resource")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	// Create a resource object for deletion
	//resource := &unstructured.Unstructured{}
	//resource.SetName(req.Name)
	//resource.SetAPIVersion(req.GVR.GroupVersion().String())
	//resource.SetKind(strings.Title(req.GVR.Resource))

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
