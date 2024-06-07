// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"time"
)

type Configuration struct {
	LogLevel     string                  `mapstructure:"logLevel"`
	ReSyncPeriod time.Duration           `mapstructure:"reSyncPeriod"`
	Resources    []ResourceConfiguration `mapstructure:"resources"`
	Store        struct {
		StoreType string                 `mapstructure:"storeType"`
		Redis     RedisConfiguration     `mapstructure:"redis"`
		Hazelcast HazelcastConfiguration `mapstructure:"hazelcast"`
	} `mapstructure:"store"`
	Fallback MongoConfiguration   `mapstructure:"fallback"`
	Metrics  MetricsConfiguration `mapstructure:"metrics"`
}

type RedisConfiguration struct {
	Host     string `mapstructure:"host"`
	Port     uint   `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	Database int    `mapstructure:"database"`
}

type HazelcastConfiguration struct {
	ClusterName string   `mapstructure:"clusterName"`
	Username    string   `mapstructure:"username"`
	Password    string   `mapstructure:"password"`
	Addresses   []string `mapstructure:"addresses"`
	WriteBehind bool     `mapstructure:"writeBehind"`
}

type MongoConfiguration struct {
	Uri      string `mapstructure:"uri"`
	Database string `mapstructure:"database"`
}

type MetricsConfiguration struct {
	Enabled bool          `mapstructure:"enabled"`
	Port    int           `mapstructure:"port"`
	Timeout time.Duration `mapstructure:"timeout"`
}
