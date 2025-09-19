// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"github.com/gofiber/fiber/v2"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strconv"
	"strings"
)

// putResource handles PUT requests to create or replace a Kubernetes resource                                                                │ │
// URL params: group, version, resource, id                                                                                                 │ │
// Request body: JSON Kubernetes resource (name/GVR must match URL)                                                                           │ │
// Response: HTTP 200 with empty body on success
func putResource(ctx *fiber.Ctx) error {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}

	id, err := getResourceNameFromContext(ctx)
	if err != nil {
		return err
	}

	resource, err := getResourceFromContext(ctx)
	if err != nil {
		return err
	}

	logRequest("Put", id, gvr, "Request received for resource")

	// Verify if url param id is present in resource
	if id != resource.GetName() {
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

	// Verify if url param resource name maps with current rule to build the dataset
	expectedResource := strings.ToLower(fmt.Sprintf("%ss", resource.GetKind()))
	if gvr.Resource != expectedResource {
		return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Resource in URL does not correlate to kind in body",
			Code:  fiber.StatusBadRequest,
		})
	}

	// Store resource
	if storeManager != nil {
		utils.AddMissingEnvironment(&resource)
		if err := storeManager.Create(&resource); err != nil {
			logger.Error().
				Err(err).
				Str("id", id).
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
	logRequest("Put", id, gvr, "Request successfully")
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

	id, err := getResourceNameFromContext(ctx)
	if err != nil {
		return err
	}

	logRequest("Get", id, gvr, "Request received for resource")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	dataset := getDatasetForGvr(gvr)
	resource, err := storeManager.Read(dataset, id)
	if err != nil {
		logger.Error().
			Err(err).
			Str("id", id).
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

	logRequest("Get", id, gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Resource: resource,
	})
}

// listResources handles GET requests to list Kubernetes resources of a specific type
// URL params: group, version, resource
// Query params: fieldSelector, limit
// Response: HTTP 200 with array of resources
func listResources(ctx *fiber.Ctx) error {

	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}
	logRequest("List-Resources", "", gvr, "Request received for resource")

	// Parse query parameters
	fieldSelector := ctx.Query("fieldSelector", "")
	limitStr := ctx.Query("limit", "")

	var limit int64 = 0
	if limitStr != "" {
		limit, err = strconv.ParseInt(limitStr, 10, 64)
		if err != nil {
			limit = 0
		}
	}

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	dataset := getDatasetForGvr(gvr)
	resources, err := storeManager.List(dataset, fieldSelector, limit)
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

	logRequest("List-Resources", "", gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Items: resources,
		Count: len(resources),
	})
}

// listKeys handles GET requests to list only the keys of a Kubernetes resources of a specific type
// URL params: group, version, resource
// Response: HTTP 200 with array of keys
func listKeys(ctx *fiber.Ctx) error {

	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}
	logRequest("List-Keys", "", gvr, "Request received for resource")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	dataset := getDatasetForGvr(gvr)
	keys, err := storeManager.Keys(dataset)
	if err != nil {
		logger.Error().
			Err(err).
			Str("group", gvr.Group).
			Str("version", gvr.Version).
			Str("resource", gvr.Resource).
			Msg("Failed to list keys")

		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error:   "Failed to list keys",
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
		})
	}

	logRequest("List-Keys", "", gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Keys: keys,
	})
}

// Count Resources handles GET requests to count the resources of a specific type
// URL params: group, version, resource
// Response: HTTP 200 with count as result
func countResources(ctx *fiber.Ctx) error {

	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}
	logRequest("Count-Resources", "", gvr, "Request received for resource")

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	dataset := getDatasetForGvr(gvr)
	count, err := storeManager.Count(dataset)
	if err != nil {
		logger.Error().
			Err(err).
			Str("group", gvr.Group).
			Str("version", gvr.Version).
			Str("resource", gvr.Resource).
			Msg("Failed to count Resources")

		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error:   "Failed to count resources",
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
		})
	}

	logRequest("Count-Resources", "", gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Count: count,
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

	id, err := getResourceNameFromContext(ctx)
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

	logRequest("Delete", id, gvr, "Request received for resource")

	// Verify if url param name is present in resource
	if id != resource.GetName() {
		return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Resource name in URL does not match resource name in body",
			Code:  fiber.StatusBadRequest,
		})
	}

	// Verify if url param resource name maps with current rule to build the store object
	expectedResource := strings.ToLower(fmt.Sprintf("%ss", resource.GetKind()))
	if gvr.Resource != expectedResource {
		return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
			Error: "Resource in URL does not correlate to kind in body",
			Code:  fiber.StatusBadRequest,
		})
	}

	if storeManager == nil {
		return ctx.Status(fiber.StatusServiceUnavailable).JSON(ErrorResponse{
			Error: "Store manager not available",
			Code:  fiber.StatusServiceUnavailable,
		})
	}

	if err := storeManager.Delete(&resource); err != nil {
		logger.Error().
			Err(err).
			Str("name", id).
			Msg("Failed to delete resource")

		return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
			Error:   "Failed to delete resource",
			Code:    fiber.StatusInternalServerError,
			Details: err.Error(),
		})
	}

	logRequest("Delete", id, gvr, "Request successfully")
	return ctx.Status(fiber.StatusNoContent).Send(nil)
}

func logRequest(operation string, id string, gvr schema.GroupVersionResource, msg string) {
	logger.Debug().
		Str("id", id).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Str("operation", operation).
		Msg(msg)
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

func getDatasetForGvr(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Group, gvr.Version)
}
