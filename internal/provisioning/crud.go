// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
)

// putResource handles PUT requests to create or replace a Kubernetes resource
// URL params: group, version, resource, id
// Request body: JSON Kubernetes resource (name/GVR must match URL)
// Response: HTTP 200 with empty body on success
func putResource(ctx *fiber.Ctx) error {
	gvr, id, resource, err := getGvrAndIdAndResourceFromContext(ctx)
	if err != nil {
		return err
	}

	logger.Debug().Fields(generateLogAttributes("Put", id, gvr)).Msg("Request received for resource")

	if err := provisioningApiStore.Create(&resource); err != nil {
		logger.Error().Err(err).Fields(generateLogAttributes("Put", id, gvr)).Msg("Failed to put resource")
		return handleInternalServerError(ctx, "Failed to put resource", err)
	}
	logger.Debug().Fields(generateLogAttributes("Put", id, gvr)).Msg("Request successfully")
	return ctx.Status(fiber.StatusOK).Send(nil)

}

// getResource handles GET requests to retrieve a specific Kubernetes resource
// URL params: group, version, resource, name
// Response: HTTP 200 with resource JSON or HTTP 404 if not found
func getResource(ctx *fiber.Ctx) error {
	gvr, id, err := getGvrAndIdFromContext(ctx)
	if err != nil {
		return err
	}

	logger.Debug().Fields(generateLogAttributes("Get", id, gvr)).Msg("Request received for resource")

	resource, err := provisioningApiStore.Read(getDataSetForGvr(gvr), id)
	if err != nil {
		logger.Error().Err(err).Fields(generateLogAttributes("Get", id, gvr)).Msg("Failed to get resource")
		return handleInternalServerError(ctx, "Failed to get resource", err)
	}

	if resource == nil {
		return handleNotFoundError(ctx, "Resource not found")
	}

	logger.Debug().Fields(generateLogAttributes("Get", id, gvr)).Msg("Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(resource)
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

	logger.Debug().Fields(generateLogAttributes("List-Resources", "", gvr)).Msg("Request received for resource")

	fieldSelector := ctx.Query("fieldSelector", "")
	limitStr := ctx.Query("limit", "")

	var limit int64 = 0
	if limitStr != "" {
		limit, err = strconv.ParseInt(limitStr, 10, 64)
		if err != nil {
			limit = 0
		}
	}

	resources, err := provisioningApiStore.List(getDataSetForGvr(gvr), fieldSelector, limit)
	if err != nil {
		logger.Error().Err(err).Fields(generateLogAttributes("List-Resources", "", gvr)).Msg("Failed to list resources")
		return handleInternalServerError(ctx, "Failed to list resources", err)
	}

	logger.Debug().Fields(generateLogAttributes("List-Resources", "", gvr)).Msg("Request successfully")
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

	logger.Debug().Fields(generateLogAttributes("List-Keys", "", gvr)).Msg("Request received for resource")

	keys, err := provisioningApiStore.Keys(getDataSetForGvr(gvr))
	if err != nil {
		logger.Error().Err(err).Fields(generateLogAttributes("List-Keys", "", gvr)).Msg("Failed to list keys")
		return handleInternalServerError(ctx, "Failed to list keys", err)
	}

	logger.Debug().Fields(generateLogAttributes("List-Keys", "", gvr)).Msg("Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Keys: keys,
	})
}

// countResources handles GET requests to count the resources of a specific type
// URL params: group, version, resource
// Response: HTTP 200 with count as result
func countResources(ctx *fiber.Ctx) error {
	gvr, err := getGvrFromContext(ctx)
	if err != nil {
		return err
	}

	logger.Debug().Fields(generateLogAttributes("Count-Resources", "", gvr)).Msg("Request received for resource")

	count, err := provisioningApiStore.Count(getDataSetForGvr(gvr))
	if err != nil {
		logger.Error().Err(err).Fields(generateLogAttributes("Count-Resources", "", gvr)).Msg("Failed to count resources")
		return handleInternalServerError(ctx, "Failed to count resources", err)
	}

	logger.Debug().Fields(generateLogAttributes("Count-Resources", "", gvr)).Msg("Request successfully")
	return ctx.Status(fiber.StatusOK).JSON(ResourceResponse{
		Count: count,
	})
}

// deleteResource handles DELETE requests to remove a Kubernetes resource
// URL params: group, version, resource, name
// Request body: JSON Kubernetes resource (name/GVR must match URL)
// Response: HTTP 204 with empty body on success
func deleteResource(ctx *fiber.Ctx) error {
	gvr, id, resource, err := getGvrAndIdAndResourceFromContext(ctx)
	if err != nil {
		return err
	}

	logger.Debug().Fields(generateLogAttributes("Delete", id, gvr)).Msg("Request received for resource")

	if err := provisioningApiStore.Delete(&resource); err != nil {
		logger.Error().Err(err).Fields(generateLogAttributes("Delete", id, gvr)).Msg("Failed to delete resource")
		return handleInternalServerError(ctx, "Failed to delete resource", err)
	}

	logger.Debug().Fields(generateLogAttributes("Delete", id, gvr)).Msg("Request successfully")
	return ctx.Status(fiber.StatusNoContent).Send(nil)
}
