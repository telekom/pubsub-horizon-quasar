// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package mongo

import (
	"errors"

	"go.mongodb.org/mongo-driver/mongo"
)

// For reference see: https://www.mongodb.com/docs/manual/reference/error-codes/
const (
	errCodeInvalidResumeToken      = 260
	errCodeChangeStreamFatal       = 280
	errCodeChangeStreamHistoryLost = 286
)

func isResumeTokenErr(err error) bool {
	if serr, ok := errors.AsType[mongo.ServerError](err); ok {
		return serr.HasErrorCode(errCodeInvalidResumeToken) ||
			serr.HasErrorCode(errCodeChangeStreamFatal) ||
			serr.HasErrorCode(errCodeChangeStreamHistoryLost)
	}

	return false
}
