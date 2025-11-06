// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

type Provisioning struct {
	Port     int                  `mapstructure:"port"`
	Security ProvisioningSecurity `mapstructure:"security"`
	LogLevel string               `mapstructure:"logLevel"`
	Store    DualStore            `mapstructure:"store"`
}

type ProvisioningSecurity struct {
	Enabled        bool     `mapstructure:"enabled"`
	TrustedIssuers []string `mapstructure:"trustedIssuers"`
	TrustedClients []string `mapstructure:"trustedClients"`
}
