// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

type ReconcileMode string

const (
	ReconcileModeIncremental ReconcileMode = "incremental"
	ReconcileModeFull        ReconcileMode = "full"
)

func (m ReconcileMode) String() string {
	switch m {
	case ReconcileModeIncremental:
		return "incremental"
	case ReconcileModeFull:
		return "full"
	default:
		return "unknown"
	}
}
