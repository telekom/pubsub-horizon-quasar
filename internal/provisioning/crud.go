// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"github.com/gofiber/fiber/v2"
	"github.com/telekom/quasar/internal/utils"
	"strconv"
)

// putResource handles PUT requests to create or replace a Kubernetes resource
// URL params: group, version, resource, id
// Request body: JSON Kubernetes resource (name/GVR must match URL)
// Response: HTTP 200 with empty body on success
func putResource(ctx *fiber.Ctx) error {
	gvr, id, resource, err := validateContextWithGvrAndIdAndResource(ctx)
	if err != nil {
		return err
	}

	logRequestDebug("Put", id, gvr, "Request received for resource")

	// Store resource
	utils.AddMissingEnvironment(&resource)
	if err := provisioningApiStore.Create(&resource); err != nil {
		logRequestError(err, "Put", id, gvr, "Failed to put resource")
		return handleInternalServerError(ctx, "Failed to put resource", err)
	}
	logRequestDebug("Put", id, gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).Send(nil)

}

// getResource handles GET requests to retrieve a specific Kubernetes resource
// URL params: group, version, resource, name
// Response: HTTP 200 with resource JSON or HTTP 404 if not found
func getResource(ctx *fiber.Ctx) error {
	gvr, id, err := validateContextWithGvrAndId(ctx)
	if err != nil {
		return err
	}

	logRequestDebug("Get", id, gvr, "Request received for resource")

	dataset := getDatasetForGvr(gvr)
	resource, err := provisioningApiStore.Read(dataset, id)
	if err != nil {
		logRequestError(err, "Get", id, gvr, "Failed to get resource")
		return handleInternalServerError(ctx, "Failed to get resource", err)
	}

	if resource == nil {
		return handleNotFoundError(ctx, "Resource not found")
	}

	logRequestDebug("Get", id, gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Resource: resource,
	})
}

// listResources handles GET requests to list Kubernetes resources of a specific type
// URL params: group, version, resource
// Query params: fieldSelector, limit
// Response: HTTP 200 with array of resources
func listResources(ctx *fiber.Ctx) error {
	gvr, err := validateContextWithGvr(ctx)
	if err != nil {
		return err
	}

	logRequestDebug("List-Resources", "", gvr, "Request received for resource")

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

	dataset := getDatasetForGvr(gvr)
	resources, err := provisioningApiStore.List(dataset, fieldSelector, limit)
	if err != nil {
		logRequestError(err, "List-Resources", "", gvr, "Failed to list resources")
		return handleInternalServerError(ctx, "Failed to list resources", err)
	}

	logRequestDebug("List-Resources", "", gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Items: resources,
		Count: len(resources),
	})
}

// listKeys handles GET requests to list only the keys of a Kubernetes resources of a specific type
// URL params: group, version, resource
// Response: HTTP 200 with array of keys
func listKeys(ctx *fiber.Ctx) error {
	gvr, err := validateContextWithGvr(ctx)
	if err != nil {
		return err
	}

	logRequestDebug("List-Keys", "", gvr, "Request received for resource")

	dataset := getDatasetForGvr(gvr)
	keys, err := provisioningApiStore.Keys(dataset)
	if err != nil {
		logRequestError(err, "List-Keys", "", gvr, "Failed to list keys")
		return handleInternalServerError(ctx, "Failed to list keys", err)
	}

	logRequestDebug("List-Keys", "", gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Keys: keys,
	})
}

// countResources handles GET requests to count the resources of a specific type
// URL params: group, version, resource
// Response: HTTP 200 with count as result
func countResources(ctx *fiber.Ctx) error {
	gvr, err := validateContextWithGvr(ctx)
	if err != nil {
		return err
	}

	logRequestDebug("Count-Resources", "", gvr, "Request received for resource")

	dataset := getDatasetForGvr(gvr)
	count, err := provisioningApiStore.Count(dataset)
	if err != nil {
		logRequestError(err, "Count-Resources", "", gvr, "Failed to count resources")
		return handleInternalServerError(ctx, "Failed to count resources", err)
	}

	logRequestDebug("Count-Resources", "", gvr, "Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Count: count,
	})
}

// deleteResource handles DELETE requests to remove a Kubernetes resource
// URL params: group, version, resource, name
// Request body: JSON Kubernetes resource (name/GVR must match URL)
// Response: HTTP 204 with empty body on success
func deleteResource(ctx *fiber.Ctx) error {
	gvr, id, resource, err := validateContextWithGvrAndIdAndResource(ctx)
	if err != nil {
		return err
	}

	logRequestDebug("Delete", id, gvr, "Request received for resource")

	if err := provisioningApiStore.Delete(&resource); err != nil {
		logRequestError(err, "Delete", id, gvr, "Failed to delete resource")
		return handleInternalServerError(ctx, "Failed to delete resource", err)
	}

	logRequestDebug("Delete", id, gvr, "Request successfully")
	return ctx.Status(fiber.StatusNoContent).Send(nil)
}
