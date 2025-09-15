// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"github.com/gofiber/fiber/v2"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func putProvision(ctx *fiber.Ctx) error {
	gvr := ctx.Locals("gvr").(schema.GroupVersionResource)
	resource, ok := ctx.Locals("resource").(unstructured.Unstructured)
	if !ok {
		return ctx.Status(fiber.StatusInternalServerError).JSON(map[string]any{
			"error": "Failed to retrieve resource from context",
		})
	}
	logger.Debug().
		Str("name", resource.GetName()).
		Str("namespace", resource.GetNamespace()).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Provisioning resource")

	if storeManager != nil {
		utils.AddMissingEnvironment(&resource)
		if err := storeManager.OnAdd(&resource); err != nil {
			logger.Error().
				Err(err).
				Str("name", resource.GetName()).
				Str("namespace", resource.GetNamespace()).
				Msg("Failed to provision resource")

			return ctx.Status(fiber.StatusInternalServerError).JSON(map[string]any{
				"error": "Failed to provision resource",
			})
		}
	}

	logger.Debug().
		Str("name", resource.GetName()).
		Str("namespace", resource.GetNamespace()).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Resource provisioned successfully")

	return ctx.SendStatus(fiber.StatusOK)
}

func deleteProvision(ctx *fiber.Ctx) error {
	gvr := ctx.Locals("gvr").(schema.GroupVersionResource)
	name, namespace := ctx.Params("name"), ctx.Params("namespace")
	logger.Debug().
		Str("name", name).
		Str("namespace", namespace).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("De-provisioning resource")

	resource, ok := ctx.Locals("resource").(unstructured.Unstructured)
	if !ok {
		return ctx.Status(fiber.StatusInternalServerError).JSON(map[string]any{
			"error": "Failed to retrieve resource from context",
		})
	}

	if storeManager != nil {
		if err := storeManager.OnDelete(&resource); err != nil {
			logger.Error().
				Err(err).
				Str("name", name).
				Str("namespace", namespace).
				Msg("Failed to de-provision resource")

			return ctx.Status(fiber.StatusInternalServerError).JSON(map[string]any{
				"error": "Failed to de-provision resource",
			})
		}
	}

	logger.Debug().
		Str("name", name).
		Str("namespace", namespace).
		Str("group", gvr.Group).
		Str("version", gvr.Version).
		Str("resource", gvr.Resource).
		Msg("Resource de-provisioned successfully")

	return ctx.SendStatus(fiber.StatusOK)
}
