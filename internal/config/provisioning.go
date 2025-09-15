// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

type ProvisioningConfiguration struct {
	Enabled  bool                              `mapstructure:"enabled"`
	Port     int                               `mapstructure:"port"`
	Security ProvisioningSecurityConfiguration `mapstructure:"security"`
	LogLevel string                            `mapstructure:"logLevel"`
	Store    StoreConfiguration                `mapstructure:"store"`
}

type ProvisioningSecurityConfiguration struct {
	Enabled        bool     `mapstructure:"enabled"`
	TrustedIssuers []string `mapstructure:"trustedIssuers"`
	TrustedClients []string `mapstructure:"trustedClients"`
}
