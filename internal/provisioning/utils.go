// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/telekom/quasar/internal/store"
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

func getStoreNameForGvr(gvr schema.GroupVersionResource) string {
	return fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Group, gvr.Version)
}

func getMongoAndHazelcastStores(dualStore store.DualStore) (mongoStore store.Store, hazelcastStore store.Store) {
	switch primary := dualStore.GetPrimary().(type) {
	case *store.MongoStore:
		mongoStore = primary
	case *store.HazelcastStore:
		hazelcastStore = primary
	case *store.RedisStore:
		logger.Warn().Msg("Primary store is Redis, not used for MongoDB/Hazelcast sync")
	}

	switch secondary := dualStore.GetSecondary().(type) {
	case *store.MongoStore:
		mongoStore = secondary
	case *store.HazelcastStore:
		hazelcastStore = secondary
	case *store.RedisStore:
		logger.Warn().Msg("Secondary store is Redis, not used for MongoDB/Hazelcast sync")
	}

	return mongoStore, hazelcastStore
}

func logStoreIdentification(mongoStore, hazelcastStore store.Store) {
	if mongoStore != nil {
		logger.Debug().Str("store", "MongoDB").Msg("Store identified")
	} else {
		logger.Warn().Msg("No MongoDB store found in DualStore configuration")
	}

	if hazelcastStore != nil {
		logger.Debug().Str("store", "Hazelcast").Msg("Store identified")
	} else {
		logger.Warn().Msg("No Hazelcast store found in DualStore configuration")
	}
}
