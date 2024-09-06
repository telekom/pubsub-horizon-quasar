// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"fmt"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/test"
	"os"
	"testing"
)

var hazelcastStore *HazelcastStore

func TestMain(m *testing.M) {
	test.SetupDocker(&test.Options{
		MongoDb:   true,
		Hazelcast: true,
	})

	hazelcastStore = new(HazelcastStore)
	config.Current = buildTestConfig()

	test.InstallLogRecorder()
	code := m.Run()

	test.TeardownDocker()
	os.Exit(code)
}

func buildTestConfig() *config.Configuration {
	var testConfig = new(config.Configuration)
	testConfig.Fallback.Mongo.Uri = fmt.Sprintf("mongodb://%s:%s", test.EnvOrDefault("MONGO_HOST", "localhost"), test.EnvOrDefault("MONGO_PORT", "27017"))
	testConfig.Fallback.Mongo.Database = "horizon"
	testConfig.Store.Hazelcast = config.HazelcastConfiguration{
		ClusterName: "horizon",
		Addresses:   []string{test.EnvOrDefault("HAZELCAST_HOST", "localhost")},
		WriteBehind: true,
	}

	var testResourceConfig = config.ResourceConfiguration{}
	testResourceConfig.Kubernetes.Group = "mygroup"
	testResourceConfig.Kubernetes.Version = "v1"
	testResourceConfig.Kubernetes.Resource = "myresource"
	testResourceConfig.Kubernetes.Namespace = "mynamespace"
	testResourceConfig.MongoIndexes = []config.MongoResourceIndex{
		{"spec.subscription.subscriptionId": 1},
	}
	testResourceConfig.HazelcastIndexes = []config.HazelcastResourceIndex{
		{
			Name:   "subscriptionId",
			Fields: []string{"spec.subscription.subscriptionId"},
			Type:   "sorted",
		},
	}

	testConfig.Resources = append(testConfig.Resources, testResourceConfig)

	return testConfig
}

func TestHazelcastStore_Initialize(t *testing.T) {
	var assertions = assert.New(t)
	assertions.NotPanics(func() {
		hazelcastStore.Initialize()
	}, "unexpected panic")
}

func TestHazelcastStore_InitializeResource(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	var testResource = config.Current.Resources[0]
	hazelcastStore.InitializeResource(&testResource)

	var errorCount = test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "unexpected errors have been logged")
}

func TestHazelcastStore_OnAdd(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	for _, subscription := range subscriptions {
		hazelcastStore.OnAdd(subscription)
		assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "could not write subscription %s", subscription.GetName())
	}
}

func TestHazelcastStore_OnUpdate(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	for _, subscription := range subscriptions {
		hazelcastStore.OnUpdate(subscription, subscription)
		assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "could not update subscription %s", subscription.GetName())
	}
}

func TestHazelcastStore_OnDelete(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	for _, subscription := range subscriptions {
		hazelcastStore.OnDelete(subscription)
		assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "could not delete subscription %s", subscription.GetName())
	}
}

func TestHazelcastStore_Shutdown(t *testing.T) {
	var assertions = assert.New(t)
	hazelcastStore.Shutdown()
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel, zerolog.WarnLevel), "shutdown produces errors and/or warnings")
}
