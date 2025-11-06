// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"errors"

	"github.com/gofiber/fiber/v2"
)

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
