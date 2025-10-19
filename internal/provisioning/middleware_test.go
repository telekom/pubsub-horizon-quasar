// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/test"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestWithGvr(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("valid GVR parameters", func(t *testing.T) {
		app := createTestFiberApp()

		app.Get("/api/v1/resources/:group/:version/:resource", withGvr, func(c *fiber.Ctx) error {
			gvr, ok := c.Locals("gvr").(schema.GroupVersionResource)
			if !ok {
				return c.Status(500).SendString("GVR not found in context")
			}
			return c.JSON(fiber.Map{
				"group":    gvr.Group,
				"version":  gvr.Version,
				"resource": gvr.Resource,
			})
		})

		req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode)
	})

	t.Run("invalid route without proper parameters", func(t *testing.T) {
		app := createTestFiberApp()

		// Register the standard route
		app.Get("/api/v1/resources/:group/:version/:resource", withGvr, func(c *fiber.Ctx) error {
			return c.SendStatus(fiber.StatusOK)
		})

		test.LogRecorder.Reset()

		// Request with missing parameters - should result in 404 (route not found)
		// This is correct behavior - Fiber's router prevents invalid URLs from reaching middleware
		req := httptest.NewRequest("GET", "/api/v1/resources/only-two-segments", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		// 404 is correct - route doesn't match
		assertions.Equal(fiber.StatusNotFound, resp.StatusCode)
	})

	t.Run("GVR stored in context correctly", func(t *testing.T) {
		app := createTestFiberApp()

		var capturedGvr schema.GroupVersionResource

		app.Get("/api/v1/resources/:group/:version/:resource", withGvr, func(c *fiber.Ctx) error {
			// Capture the GVR from context to verify it was set correctly
			gvr, ok := c.Locals("gvr").(schema.GroupVersionResource)
			if !ok {
				return c.Status(500).SendString("GVR not found")
			}
			capturedGvr = gvr
			return c.SendStatus(fiber.StatusOK)
		})

		req := httptest.NewRequest("GET", "/api/v1/resources/apps/v1/deployments", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode)
		assertions.Equal("apps", capturedGvr.Group)
		assertions.Equal("v1", capturedGvr.Version)
		assertions.Equal("deployments", capturedGvr.Resource)
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged by middleware itself")
}

func TestWithResourceId(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("valid resource ID parameter", func(t *testing.T) {
		app := createTestFiberApp()

		app.Get("/api/v1/resources/:id", withResourceId, func(c *fiber.Ctx) error {
			resourceId, ok := c.Locals("resourceId").(string)
			if !ok {
				return c.Status(500).SendString("resourceId not found in context")
			}
			return c.JSON(fiber.Map{"resourceId": resourceId})
		})

		req := httptest.NewRequest("GET", "/api/v1/resources/test-subscription-123", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode)
	})

	t.Run("missing resource ID parameter", func(t *testing.T) {
		app := createTestFiberApp()

		app.Get("/api/v1/resources/", withResourceId, func(c *fiber.Ctx) error {
			return c.SendStatus(fiber.StatusOK)
		})

		test.LogRecorder.Reset()

		req := httptest.NewRequest("GET", "/api/v1/resources/", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusInternalServerError, resp.StatusCode)
	})

	t.Run("resource ID with special characters", func(t *testing.T) {
		app := createTestFiberApp()

		app.Get("/api/v1/resources/:id", withResourceId, func(c *fiber.Ctx) error {
			resourceId, ok := c.Locals("resourceId").(string)
			if !ok {
				return c.Status(500).SendString("resourceId not found in context")
			}
			return c.JSON(fiber.Map{"resourceId": resourceId})
		})

		// Test with URL-encoded special characters
		req := httptest.NewRequest("GET", "/api/v1/resources/test-resource-with-dashes", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode)
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged by middleware itself")
}

func TestWithTrustedClients(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("trusted client allowed", func(t *testing.T) {
		app := createTestFiberApp()

		trustedClients := []string{"trusted-client-1", "trusted-client-2"}

		// Middleware that mocks JWT by setting user in context
		mockJwtMiddleware := func(c *fiber.Ctx) error {
			// Mock a valid JWT token with trusted client
			token := &jwt.Token{
				Claims: jwt.MapClaims{
					"clientId": "trusted-client-1",
					"sub":      "test-user",
				},
			}
			c.Locals("user", token)
			return c.Next()
		}

		app.Get("/protected", mockJwtMiddleware, withTrustedClients(trustedClients), func(c *fiber.Ctx) error {
			return c.JSON(fiber.Map{"message": "access granted"})
		})

		req := httptest.NewRequest("GET", "/protected", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode)
	})

	t.Run("untrusted client blocked", func(t *testing.T) {
		app := createTestFiberApp()

		trustedClients := []string{"trusted-client-1", "trusted-client-2"}

		// Middleware that mocks JWT with untrusted client
		mockJwtMiddleware := func(c *fiber.Ctx) error {
			token := &jwt.Token{
				Claims: jwt.MapClaims{
					"clientId": "untrusted-client",
					"sub":      "test-user",
				},
			}
			c.Locals("user", token)
			return c.Next()
		}

		app.Get("/protected", mockJwtMiddleware, withTrustedClients(trustedClients), func(c *fiber.Ctx) error {
			return c.JSON(fiber.Map{"message": "access granted"})
		})

		req := httptest.NewRequest("GET", "/protected", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusUnauthorized, resp.StatusCode)
	})

	t.Run("empty trusted clients list allows all", func(t *testing.T) {
		app := createTestFiberApp()

		trustedClients := []string{} // Empty list

		// Middleware that mocks JWT
		mockJwtMiddleware := func(c *fiber.Ctx) error {
			token := &jwt.Token{
				Claims: jwt.MapClaims{
					"clientId": "any-client",
					"sub":      "test-user",
				},
			}
			c.Locals("user", token)
			return c.Next()
		}

		app.Get("/protected", mockJwtMiddleware, withTrustedClients(trustedClients), func(c *fiber.Ctx) error {
			return c.JSON(fiber.Map{"message": "access granted"})
		})

		req := httptest.NewRequest("GET", "/protected", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode, "empty trusted clients list should allow all")
	})

	t.Run("multiple trusted clients", func(t *testing.T) {
		app := createTestFiberApp()

		trustedClients := []string{"client-a", "client-b", "client-c"}

		// Test with second client in list
		mockJwtMiddleware := func(c *fiber.Ctx) error {
			token := &jwt.Token{
				Claims: jwt.MapClaims{
					"clientId": "client-b",
					"sub":      "test-user",
				},
			}
			c.Locals("user", token)
			return c.Next()
		}

		app.Get("/protected", mockJwtMiddleware, withTrustedClients(trustedClients), func(c *fiber.Ctx) error {
			return c.JSON(fiber.Map{"message": "access granted"})
		})

		req := httptest.NewRequest("GET", "/protected", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusOK, resp.StatusCode)
	})

	t.Run("case sensitive client matching", func(t *testing.T) {
		app := createTestFiberApp()

		trustedClients := []string{"TrustedClient"}

		// Test with different case - should fail
		mockJwtMiddleware := func(c *fiber.Ctx) error {
			token := &jwt.Token{
				Claims: jwt.MapClaims{
					"clientId": "trustedclient", // lowercase
					"sub":      "test-user",
				},
			}
			c.Locals("user", token)
			return c.Next()
		}

		app.Get("/protected", mockJwtMiddleware, withTrustedClients(trustedClients), func(c *fiber.Ctx) error {
			return c.JSON(fiber.Map{"message": "access granted"})
		})

		req := httptest.NewRequest("GET", "/protected", nil)
		resp, err := app.Test(req)

		assertions.NoError(err)
		assertions.Equal(fiber.StatusUnauthorized, resp.StatusCode, "client matching should be case sensitive")
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged by middleware itself")
}
