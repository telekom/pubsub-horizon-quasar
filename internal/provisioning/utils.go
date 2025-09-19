// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"github.com/gofiber/fiber/v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func logRequestDebug(operation string, id string, gvr schema.GroupVersionResource, msg string) {
	logger.Debug().
		Str("id", id).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Str("operation", operation).
		Msg(msg)
}

func logRequestError(err error, operation string, id string, gvr schema.GroupVersionResource, msg string) {
	logger.Error().Err(err).
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
			handleInternalServerError(ctx, "Failed to retrieve group, version and resource from context",
				fmt.Errorf("invalid or missing GVR in context"))
	}
	return gvr, nil
}

func getResourceIdFromContext(ctx *fiber.Ctx) (string, error) {
	name, ok := ctx.Locals("resourceId").(string)
	if !ok || name == "" {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve Resource Id from context")

		return "",
			handleInternalServerError(ctx, "Failed to retrieve resource id from context",
				fmt.Errorf("invalid or missing resource id in context"))
	}
	return name, nil
}

func getResourceFromContext(ctx *fiber.Ctx) (unstructured.Unstructured, error) {
	resource, ok := ctx.Locals("resource").(unstructured.Unstructured)
	if !ok {
		logger.Warn().
			Str("group", ctx.Request().URI().String()).
			Msg("Failed to retrieve resource from context")

		return unstructured.Unstructured{},
			handleInternalServerError(ctx, "Failed to retrieve resource from context",
				fmt.Errorf("invalid or missing resource in context"))
	}
	return resource, nil
}

func getDatasetForGvr(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Group, gvr.Version)
}
