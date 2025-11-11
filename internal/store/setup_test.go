// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/hazelcast/hazelcast-go-client"
	"github.com/hazelcast/hazelcast-go-client/serialization"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/test"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
)

var hazelcastStore *HazelcastStore

// TestMain sets up the test environment for all store tests.
// This is a package-level setup that runs before all tests in this package.
// NOTE: This creates a global hazelcastStore instance that is shared across all tests.
// Tests that modify the store state should reset it in defer blocks or at the start.
func TestMain(m *testing.M) {
	// Setup Docker containers for MongoDB and Hazelcast
	test.SetupDocker(&test.Options{
		MongoDb:   true,
		Hazelcast: true,
	})

	// Initialize the global hazelcast store instance
	hazelcastStore = new(HazelcastStore)
	config.Current = buildTestConfig()

	// Install log recorder to capture log output in tests
	test.InstallLogRecorder()

	// Run all tests in this package
	code := m.Run()

	// Cleanup
	test.TeardownDocker()
	os.Exit(code)
}

func buildTestConfig() *config.Configuration {
	// Use shared base configuration (MongoDB + Hazelcast)
	testConfig := test.BuildBaseTestConfig()

	// Add test resource with indexes for store tests
	test.AddTestResourceWithIndexes(
		testConfig,
		"mygroup",     // group
		"v1",          // version
		"myresource",  // resource
		"",            // kind (not needed for store tests)
		"mynamespace", // namespace
		[]config.MongoResourceIndex{
			{"spec.subscription.subscriptionId": 1},
		},
		[]config.HazelcastResourceIndex{
			{
				Name:   "subscriptionId",
				Fields: []string{"spec.subscription.subscriptionId"},
				Type:   "sorted",
			},
		},
	)

	return testConfig
}

// createFakeDynamicClient creates a fake Kubernetes dynamic client for testing.
func createFakeDynamicClient() dynamic.Interface {
	subscriptions := test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	scheme := runtime.NewScheme()

	// Create a mapping for list kinds to support reconciliation List() operations
	listKinds := map[schema.GroupVersionResource]string{
		{
			Group:    "mygroup",
			Version:  "v1",
			Resource: "myresource",
		}: "MyResourceList",
		{
			Group:    "subscriber.horizon.telekom.de",
			Version:  "v1",
			Resource: "subscriptions",
		}: "SubscriptionList",
	}

	return fake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, subscriptions[0], subscriptions[1])
}

// getMapItem is a test helper to retrieve and deserialize an item from a Hazelcast map.
func getMapItem(assertions *assert.Assertions, hzMap *hazelcast.Map, key any) *unstructured.Unstructured {
	data, err := hzMap.Get(context.Background(), key)
	assertions.NoError(err, "could not get subscription %s", key)

	jsonData := data.(serialization.JSON)

	unmarshalledData := make(map[string]any)
	err = json.Unmarshal(jsonData, &unmarshalledData)
	assertions.NoError(err, "could not unmarshal subscription %s", key)

	obj := new(unstructured.Unstructured)
	obj.Object = unmarshalledData

	return obj
}
