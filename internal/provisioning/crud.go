// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"github.com/gofiber/fiber/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

	if _, err := KubernetesClient.Resource(gvr).Namespace(resource.GetNamespace()).Apply(ctx.UserContext(), resource.GetName(), &resource, metav1.ApplyOptions{
		FieldManager: "kubectl-client-side-apply",
	}); err != nil {
		logger.Error().
			Err(err).
			Str("name", resource.GetName()).
			Str("namespace", resource.GetNamespace()).
			Msg("Failed to provision resource")

		return ctx.Status(fiber.StatusInternalServerError).JSON(map[string]any{
			"error": "Failed to provision resource",
		})
	}

	return ctx.SendStatus(fiber.StatusOK)
}

func deleteProvision(ctx *fiber.Ctx) error {
	gvr := ctx.Locals("gvr").(schema.GroupVersionResource)
	name, namespace := ctx.Params("name"), ctx.Params("namespace")
	logger.Debug().
		Str("name", name).
		Str("namespace", namespace).
		Msg("De-provisioning resource")

	if err := KubernetesClient.Resource(gvr).Namespace(namespace).Delete(context.Background(), name, metav1.DeleteOptions{}); err != nil {
		logger.Error().
			Err(err).
			Str("name", name).
			Str("namespace", namespace).
			Msg("Failed to de-provision resource")

		if errors.IsNotFound(err) {
			return ctx.Status(fiber.StatusNotFound).JSON(map[string]any{
				"message": "Resource not found",
			})
		}

		return ctx.Status(fiber.StatusInternalServerError).JSON(map[string]any{
			"error": "Failed to de-provision resource",
		})
	}

	return ctx.SendStatus(fiber.StatusOK)
}
