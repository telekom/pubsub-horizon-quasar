// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

import "time"

type Snapshot struct {
	Enabled         bool              `mapstructure:"enabled"`
	RebuildInterval time.Duration     `mapstructure:"rebuildInterval"`
	ActivationDelay time.Duration     `mapstructure:"activationDelay"`
	CleanupInterval time.Duration     `mapstructure:"cleanupInterval"`
	Mongo           SnapshotMongo     `mapstructure:"mongo"`
	ZooKeeper       SnapshotZooKeeper `mapstructure:"zookeeper"`
}

type SnapshotMongo struct {
	Uri               string        `mapstructure:"uri"`
	Database          string        `mapstructure:"database"`
	SourceCollection  string        `mapstructure:"sourceCollection"`
	TargetCollection  string        `mapstructure:"targetCollection"`
	ReconnectInterval time.Duration `mapstructure:"reconnectInterval"`
}

type SnapshotZooKeeper struct {
	Addresses      []string      `mapstructure:"addresses"`
	SessionTimeout time.Duration `mapstructure:"sessionTimeout"`
}
