// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"slices"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
		log.Error().Err(err).Msg("Failed to unmarshal JSON body: No valid Kubernetes resource provided.")
		return handleBadRequestError(ctx, "Invalid JSON body: No valid Kubernetes resource provided")
	}
	utils.AddMissingEnvironment(resource)

	ctx.Locals("resource", *resource)
	return ctx.Next()
}

func withGvr(ctx *fiber.Ctx) error {
	group, version, resource := ctx.Params("group"), ctx.Params("version"), ctx.Params("resource")

	// check if the provided group/version/resource exists in the configuration
	found := slices.ContainsFunc(config.Current.Resources, func(res config.Resource) bool {
		return res.Kubernetes.Group == group &&
			res.Kubernetes.Version == version &&
			res.Kubernetes.Resource == resource
	})

	if !found {
		log.Debug().Msgf("Unsupported group, version, or resource in request path: %s/%s/%s", group, version, resource)
		return handleBadRequestError(ctx, "Unsupported group, version, or resource in request path")
	}

	ctx.Locals("gvr", schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	})
	return ctx.Next()
}

func withResourceId(ctx *fiber.Ctx) error {
	id := ctx.Params("id")

	if id == "" {
		return handleInternalServerError(ctx, "Failed to retrieve resource id from request",
			fmt.Errorf("missing required URL parameter: id"))
	}
	ctx.Locals("resourceId", id)
	return ctx.Next()
}
