// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import "errors"

var (
	ErrUnknownStoreType = errors.New("unknown store type")
	ErrResourceNotFound = errors.New("resource not found")
)
