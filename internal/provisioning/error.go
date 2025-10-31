// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"errors"

	"github.com/gofiber/fiber/v2"
)

// handleInternalServerError returns a standardized internal server error response
func handleInternalServerError(ctx *fiber.Ctx, message string, err error) error {
	return ctx.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{
		Error:   message,
		Code:    fiber.StatusInternalServerError,
		Details: err.Error(),
	})
}

// handleBadRequestError returns a standardized bad request error response
func handleBadRequestError(ctx *fiber.Ctx, message string) error {
	return ctx.Status(fiber.StatusBadRequest).JSON(ErrorResponse{
		Error: message,
		Code:  fiber.StatusBadRequest,
	})
}

// handleNotFoundError returns a standardized not found error response
func handleNotFoundError(ctx *fiber.Ctx, message string) error {
	return ctx.Status(fiber.StatusNotFound).JSON(ErrorResponse{
		Error: message,
		Code:  fiber.StatusNotFound,
	})
}

func handleErrors(ctx *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError

	var fiberErr *fiber.Error
	if ok := errors.As(err, &fiberErr); ok {
		code = fiberErr.Code
	}

	return ctx.Status(code).JSON(fiber.Map{
		"error": err.Error(),
		"code":  code,
	})
}
