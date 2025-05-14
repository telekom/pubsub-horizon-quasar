// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package config

type ReconcileMode string

const (
	Incremental ReconcileMode = "incremental"
	Full        ReconcileMode = "full"
)

func (m ReconcileMode) String() string {
	switch m {
	case Incremental:
		return "incremental"
	case Full:
		return "full"
	default:
		return "unknown"
	}
}
