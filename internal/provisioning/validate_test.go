// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/test"
	"github.com/valyala/fasthttp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func createTestFiberApp() *fiber.App {
	return fiber.New()
}

func TestValidateResourceId(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("matching resource ID", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetName("test-resource")

		err := validateResourceId("test-resource", *resource)
		assertions.NoError(err)
	})

	t.Run("non-matching resource ID", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetName("test-resource")

		err := validateResourceId("different-resource", *resource)
		assertions.Error(err, "should return an error")
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged")
}

func TestValidateResourceGVR(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("matching GVR", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetAPIVersion("subscriber.horizon.telekom.de/v1")

		gvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		err := validateResourceApiVersion(gvr, *resource)
		assertions.NoError(err)
	})

	t.Run("non-matching GVR", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetAPIVersion("different.group/v2")

		gvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		err := validateResourceApiVersion(gvr, *resource)
		assertions.Error(err, "should return an error")

		// Verify it's a fiber error with the correct status code
		var fiberErr *fiber.Error
		assertions.ErrorAs(err, &fiberErr)
		assertions.Equal(fiber.StatusBadRequest, fiberErr.Code, "should return BadRequest error code")
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged")
}

func TestValidateResourceKind(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	config.Current = test.CreateTestResourceConfig()

	app := createTestFiberApp()

	t.Run("matching kind", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetKind("Subscription")

		gvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		err := validateResourceKind(gvr, *resource)
		assertions.NoError(err)
	})

	t.Run("non-matching kind", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetKind("DifferentKind")

		gvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		err := validateResourceKind(gvr, *resource)
		assertions.Error(err, "should return an error")

		// Verify it's a fiber error with the correct status code
		var fiberErr *fiber.Error
		assertions.ErrorAs(err, &fiberErr)
		assertions.Equal(fiber.StatusBadRequest, fiberErr.Code, "should return BadRequest error code")
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged")
}

func TestValidateContextWithGvr(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("valid GVR in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}
		ctx.Locals("gvr", expectedGvr)

		gvr, err := getGvrFromContext(ctx)
		assertions.NoError(err)
		assertions.Equal(expectedGvr, gvr)
	})

	t.Run("missing GVR in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		getGvrFromContext(ctx)
		assertions.Equal(fiber.StatusInternalServerError, ctx.Response().StatusCode(), "should return InternalServerError status")
	})

	t.Run("invalid GVR in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		// Set invalid GVR (missing required fields)
		ctx.Locals("gvr", schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "",
			Resource: "",
		})

		getGvrFromContext(ctx)
		assertions.Equal(fiber.StatusInternalServerError, ctx.Response().StatusCode(), "should return InternalServerError status")
	})
}

func TestValidateContextWithGvrAndId(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("valid GVR and ID in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}
		ctx.Locals("gvr", expectedGvr)
		ctx.Locals("resourceId", "test-resource")

		gvr, id, err := getGvrAndIdFromContext(ctx)
		assertions.NoError(err)
		assertions.Equal(expectedGvr, gvr)
		assertions.Equal("test-resource", id)
	})

	t.Run("missing ID in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}
		ctx.Locals("gvr", expectedGvr)

		getGvrAndIdFromContext(ctx)
		assertions.Equal(fiber.StatusInternalServerError, ctx.Response().StatusCode(), "should return InternalServerError status")
	})
}

func TestValidateContextWithGvrAndIdAndResource(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("valid complete validation", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		config.Current = test.CreateTestResourceConfig()

		resource := &unstructured.Unstructured{}
		resource.SetName("test-subscription")
		resource.SetKind("Subscription")
		resource.SetAPIVersion("subscriber.horizon.telekom.de/v1")

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		ctx.Locals("gvr", expectedGvr)
		ctx.Locals("resourceId", "test-subscription")
		ctx.Locals("resource", *resource)

		gvr, id, res, err := getGvrAndIdAndResourceFromContext(ctx)
		assertions.NoError(err)
		assertions.Equal(expectedGvr, gvr)
		assertions.Equal("test-subscription", id)
		assertions.Equal("test-subscription", res.GetName())
	})

	t.Run("mismatched resource name", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetName("different-name")
		resource.SetKind("Subscription")
		resource.SetAPIVersion("subscriber.horizon.telekom.de/v1")

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		ctx.Locals("gvr", expectedGvr)
		ctx.Locals("resourceId", "test-subscription")
		ctx.Locals("resource", *resource)

		_, _, _, err := getGvrAndIdAndResourceFromContext(ctx)
		handleErrors(ctx, err)
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return BadRequest status")
	})

	t.Run("mismatched GVR", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetName("test-subscription")
		resource.SetKind("Subscription")
		resource.SetAPIVersion("different.group/v2")

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		ctx.Locals("gvr", expectedGvr)
		ctx.Locals("resourceId", "test-subscription")
		ctx.Locals("resource", *resource)

		_, _, _, err := getGvrAndIdAndResourceFromContext(ctx)
		handleErrors(ctx, err)
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return BadRequest status")
	})

	t.Run("mismatched kind", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		resource := &unstructured.Unstructured{}
		resource.SetName("test-subscription")
		resource.SetKind("DifferentKind")
		resource.SetAPIVersion("subscriber.horizon.telekom.de/v1")

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}

		ctx.Locals("gvr", expectedGvr)
		ctx.Locals("resourceId", "test-subscription")
		ctx.Locals("resource", *resource)

		_, _, _, err := getGvrAndIdAndResourceFromContext(ctx)
		handleErrors(ctx, err)
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return BadRequest status")
	})
}

func TestGetGvrFromContext(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("valid GVR in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		expectedGvr := schema.GroupVersionResource{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}
		ctx.Locals("gvr", expectedGvr)

		gvr, err := getGvrFromContext(ctx)
		assertions.NoError(err)
		assertions.Equal(expectedGvr, gvr)
	})

	t.Run("missing GVR in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		_, _, _, err := getGvrAndIdAndResourceFromContext(ctx)
		handleErrors(ctx, err)
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return InternalServerError status")
	})
}

func TestGetResourceIdFromContext(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("valid resource ID in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		ctx.Locals("resourceId", "test-resource")

		id, err := getResourceIdFromContext(ctx)
		assertions.NoError(err)
		assertions.Equal("test-resource", id)
	})

	t.Run("missing resource ID in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		_, _, _, err := getGvrAndIdAndResourceFromContext(ctx)
		handleErrors(ctx, err)
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return InternalServerError status")
	})

	t.Run("empty resource ID in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		ctx.Locals("resourceId", "")

		_, _, _, err := getGvrAndIdAndResourceFromContext(ctx)
		handleErrors(ctx, err)
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return InternalServerError status")
	})
}

func TestGetResourceFromContext(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("valid resource in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		expectedResource := &unstructured.Unstructured{}
		expectedResource.SetName("test-resource")

		ctx.Locals("resource", *expectedResource)

		resource, err := getResourceFromContext(ctx)
		assertions.NoError(err)
		assertions.Equal("test-resource", resource.GetName())
	})

	t.Run("missing resource in context", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		getResourceFromContext(ctx)
		assertions.Equal(fiber.StatusInternalServerError, ctx.Response().StatusCode(), "should return InternalServerError status")
	})
}

func createTestResourceJSON(name, kind, apiVersion string) []byte {
	resource := map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name": name,
		},
	}
	data, _ := json.Marshal(resource)
	return data
}

func TestLogRequestDebug(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	gvr := schema.GroupVersionResource{
		Group:    "subscriber.horizon.telekom.de",
		Version:  "v1",
		Resource: "subscriptions",
	}

	// Reset log recorder
	test.LogRecorder.Reset()

	assertions.NotPanics(func() {
		logRequestDebug("TestOperation", "test-id", gvr, "Test message")
	})

	// Should log at debug level
	debugCount := test.LogRecorder.GetRecordCount(zerolog.DebugLevel)
	assertions.GreaterOrEqual(debugCount, 1, "should log at debug level")
}

func TestLogRequestError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	gvr := schema.GroupVersionResource{
		Group:    "subscriber.horizon.telekom.de",
		Version:  "v1",
		Resource: "subscriptions",
	}

	// Reset log recorder
	test.LogRecorder.Reset()

	assertions.NotPanics(func() {
		logRequestError(assert.AnError, "TestOperation", "test-id", gvr, "Test error message")
	})

	// Should log at error level
	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.GreaterOrEqual(errorCount, 1, "should log at error level")
}

func TestWithKubernetesResource(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("valid JSON resource", func(t *testing.T) {
		app := createTestFiberApp()

		// Create a test route with the middleware
		app.Post("/test", withKubernetesResource, func(c *fiber.Ctx) error {
			// Handler after middleware - verify resource was stored
			resource, ok := c.Locals("resource").(unstructured.Unstructured)
			if !ok {
				return c.Status(500).SendString("Resource not found in context")
			}
			return c.JSON(fiber.Map{"name": resource.GetName()})
		})

		resourceData := createTestResourceJSON("test-resource", "Subscription", "subscriber.horizon.telekom.de/v1")
		req := httptest.NewRequest("POST", "/test", bytes.NewReader(resourceData))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode, "should return OK status")

		// Parse response to verify resource name
		var response map[string]string
		json.NewDecoder(resp.Body).Decode(&response)
		assertions.Equal("test-resource", response["name"])
	})

	t.Run("invalid JSON", func(t *testing.T) {
		app := createTestFiberApp()

		// Create a test route with the middleware
		app.Post("/test", withKubernetesResource, func(c *fiber.Ctx) error {
			return c.SendStatus(fiber.StatusOK)
		})

		// Reset log recorder for this test
		test.LogRecorder.Reset()

		req := httptest.NewRequest("POST", "/test", bytes.NewReader([]byte("invalid json")))
		req.Header.Set("Content-Type", "application/json")

		resp, err := app.Test(req)
		assertions.NoError(err)
		assertions.Equal(fiber.StatusBadRequest, resp.StatusCode, "should return BadRequest status")
	})
}
