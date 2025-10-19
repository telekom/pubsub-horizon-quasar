// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/test"
	"github.com/valyala/fasthttp"
)

func TestHandleInternalServerError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("internal server error response", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		testError := errors.New("test error message")
		handleInternalServerError(ctx, "Something went wrong", testError)

		// Verify HTTP status code
		assertions.Equal(fiber.StatusInternalServerError, ctx.Response().StatusCode(), "should return InternalServerError status")

		// Verify response body structure
		var response ErrorResponse
		jsonErr := json.Unmarshal(ctx.Response().Body(), &response)
		assertions.NoError(jsonErr)
		assertions.Equal("Something went wrong", response.Error)
		assertions.Equal(fiber.StatusInternalServerError, response.Code)
		assertions.Equal("test error message", response.Details)
	})

	t.Run("internal server error with different message", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		testError := errors.New("database connection failed")
		handleInternalServerError(ctx, "Database error", testError)

		// Verify HTTP status code
		assertions.Equal(fiber.StatusInternalServerError, ctx.Response().StatusCode(), "should return InternalServerError status")

		var response ErrorResponse
		jsonErr := json.Unmarshal(ctx.Response().Body(), &response)
		assertions.NoError(jsonErr)
		assertions.Equal("Database error", response.Error)
		assertions.Equal("database connection failed", response.Details)
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged by error handlers themselves")
}

func TestHandleBadRequestError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("bad request error response", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		handleBadRequestError(ctx, "Invalid input")

		// Verify HTTP status code
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return BadRequest status")

		// Verify response body structure
		var response ErrorResponse
		jsonErr := json.Unmarshal(ctx.Response().Body(), &response)
		assertions.NoError(jsonErr)
		assertions.Equal("Invalid input", response.Error)
		assertions.Equal(fiber.StatusBadRequest, response.Code)
		assertions.Equal("", response.Details, "bad request error should not have details")
	})

	t.Run("bad request error with different message", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		handleBadRequestError(ctx, "Missing required field")

		// Verify HTTP status code
		assertions.Equal(fiber.StatusBadRequest, ctx.Response().StatusCode(), "should return BadRequest status")

		var response ErrorResponse
		jsonErr := json.Unmarshal(ctx.Response().Body(), &response)
		assertions.NoError(jsonErr)
		assertions.Equal("Missing required field", response.Error)
		assertions.Equal(fiber.StatusBadRequest, response.Code)
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged by error handlers themselves")
}

func TestHandleNotFoundError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := createTestFiberApp()

	t.Run("not found error response", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		handleNotFoundError(ctx, "Resource not found")

		// Verify HTTP status code
		assertions.Equal(fiber.StatusNotFound, ctx.Response().StatusCode(), "should return NotFound status")

		// Verify response body structure
		var response ErrorResponse
		jsonErr := json.Unmarshal(ctx.Response().Body(), &response)
		assertions.NoError(jsonErr)
		assertions.Equal("Resource not found", response.Error)
		assertions.Equal(fiber.StatusNotFound, response.Code)
		assertions.Equal("", response.Details, "not found error should not have details")
	})

	t.Run("not found error with different message", func(t *testing.T) {
		ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
		defer app.ReleaseCtx(ctx)

		handleNotFoundError(ctx, "Subscription does not exist")

		// Verify HTTP status code
		assertions.Equal(fiber.StatusNotFound, ctx.Response().StatusCode(), "should return NotFound status")

		var response ErrorResponse
		jsonErr := json.Unmarshal(ctx.Response().Body(), &response)
		assertions.NoError(jsonErr)
		assertions.Equal("Subscription does not exist", response.Error)
		assertions.Equal(fiber.StatusNotFound, response.Code)
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged by error handlers themselves")
}

func TestErrorResponseStructure(t *testing.T) {
	var assertions = assert.New(t)

	t.Run("error response JSON marshaling", func(t *testing.T) {
		response := ErrorResponse{
			Error:   "Test error",
			Code:    500,
			Details: "Test details",
		}

		data, err := json.Marshal(response)
		assertions.NoError(err)

		var unmarshaled ErrorResponse
		err = json.Unmarshal(data, &unmarshaled)
		assertions.NoError(err)

		assertions.Equal("Test error", unmarshaled.Error)
		assertions.Equal(500, unmarshaled.Code)
		assertions.Equal("Test details", unmarshaled.Details)
	})

	t.Run("error response without details", func(t *testing.T) {
		response := ErrorResponse{
			Error: "Test error",
			Code:  400,
		}

		data, err := json.Marshal(response)
		assertions.NoError(err)

		var unmarshaled ErrorResponse
		err = json.Unmarshal(data, &unmarshaled)
		assertions.NoError(err)

		assertions.Equal("Test error", unmarshaled.Error)
		assertions.Equal(400, unmarshaled.Code)
		assertions.Equal("", unmarshaled.Details)
	})
}
