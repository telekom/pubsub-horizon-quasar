// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"slices"
)

func withTrustedClients(trustedClients []string) fiber.Handler {
	return func(ctx *fiber.Ctx) error {
		if len(trustedClients) > 0 {
			user := ctx.Locals("user").(*jwt.Token)
			claims := user.Claims.(jwt.MapClaims)
			clientId := claims["clientId"].(string)
			if !slices.Contains(trustedClients, clientId) {
				return &fiber.Error{Code: fiber.StatusUnauthorized, Message: "Unauthorized client"}
			}
		}

		return ctx.Next()
	}
}

func withKubernetesResource(ctx *fiber.Ctx) error {
	resource := new(unstructured.Unstructured)
	if err := resource.UnmarshalJSON(ctx.Body()); err != nil {
		log.Error().Err(err).Msg("Failed to unmarshal JSON body into Kubernetes resource")
		return ctx.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid JSON body",
		})
	}

	ctx.Locals("resource", *resource)
	return ctx.Next()
}

func withGvr(ctx *fiber.Ctx) error {
	group, version, resource := ctx.Params("group"), ctx.Params("version"), ctx.Params("resource")
	ctx.Locals("gvr", schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	})
	return ctx.Next()
}
