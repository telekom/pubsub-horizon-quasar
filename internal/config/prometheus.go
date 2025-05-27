// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

type PrometheusConfiguration struct {
	Enabled bool              `mapstructure:"enabled"`
	Labels  map[string]string `mapstructure:"labels"`
}
