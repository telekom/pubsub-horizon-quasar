// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"fmt"
	"net"

	"github.com/telekom/quasar/internal/config"
)

const defaultHorizonName = "horizon"

// BuildBaseTestConfig creates a base test configuration with MongoDB and Hazelcast setup.
// This configuration can be extended by individual test packages as needed.
func BuildBaseTestConfig() *config.Configuration {
	testConfig := new(config.Configuration)

	// MongoDB configuration
	mongoHost := EnvOrDefault("MONGO_HOST", "localhost")
	mongoPort := EnvOrDefault("MONGO_PORT", "27017")
	mongoUri := fmt.Sprintf("mongodb://%s", net.JoinHostPort(mongoHost, mongoPort))

	testConfig.Fallback.Mongo.Uri = mongoUri
	testConfig.Fallback.Mongo.Database = defaultHorizonName

	// MongoDB Store configuration
	testConfig.Store.Mongo.Uri = mongoUri
	testConfig.Store.Mongo.Database = defaultHorizonName

	// Hazelcast configuration
	testConfig.Store.Hazelcast = config.Hazelcast{
		ClusterName: defaultHorizonName,
		Addresses:   []string{EnvOrDefault("HAZELCAST_HOST", "localhost")},
	}
	testConfig.Store.Hazelcast.ReconcileMode = config.ReconcileModeIncremental

	return testConfig
}

// AddTestResource adds a standard test resource configuration.
// This is a helper for tests that need a basic resource configuration.
func AddTestResource(cfg *config.Configuration, group, version, resource, kind, namespace string) {
	resourceConfig := config.Resource{}
	resourceConfig.Kubernetes.Group = group
	resourceConfig.Kubernetes.Version = version
	resourceConfig.Kubernetes.Resource = resource
	resourceConfig.Kubernetes.Kind = kind
	resourceConfig.Kubernetes.Namespace = namespace

	cfg.Resources = append(cfg.Resources, resourceConfig)
}

// AddTestResourceWithIndexes adds a test resource with MongoDB and Hazelcast indexes.
func AddTestResourceWithIndexes(
	cfg *config.Configuration,
	group, version, resource, kind, namespace string,
	mongoIndexes []config.MongoResourceIndex,
	hazelcastIndexes []config.HazelcastResourceIndex,
) {
	resourceConfig := config.Resource{}
	resourceConfig.Kubernetes.Group = group
	resourceConfig.Kubernetes.Version = version
	resourceConfig.Kubernetes.Resource = resource
	resourceConfig.Kubernetes.Kind = kind
	resourceConfig.Kubernetes.Namespace = namespace
	resourceConfig.MongoIndexes = mongoIndexes
	resourceConfig.HazelcastIndexes = hazelcastIndexes

	cfg.Resources = append(cfg.Resources, resourceConfig)
}
