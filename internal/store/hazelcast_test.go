// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hazelcast/hazelcast-go-client/serialization"
	"github.com/telekom/quasar/internal/reconciliation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"

	"github.com/hazelcast/hazelcast-go-client"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/test"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
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
	testConfig.Store.Hazelcast.ReconcileMode = config.ReconcileModeIncremental

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
	hazelcastStore.InitializeResource(createFakeDynamicClient(), &testResource)

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

		hzMap := hazelcastStore.getMap(subscription)

		ok, err := hzMap.ContainsKey(context.Background(), subscription.GetName())
		assertions.NoError(err, "could not lookup subscription %s", subscription.GetName())
		assertions.True(ok, "subscription %s not found in map", subscription.GetName())
	}
}

func TestHazelcastStore_OnUpdate(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	for _, subscription := range subscriptions {

		updatedSubscription := subscription.DeepCopy()
		labels := make(map[string]string)
		labels["hazelcast_test"] = "true"
		updatedSubscription.SetLabels(labels)

		hazelcastStore.OnUpdate(subscription, updatedSubscription)
		assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "could not update subscription %s", subscription.GetName())

		hzMap := hazelcastStore.getMap(subscription)

		ok, err := hzMap.ContainsKey(context.Background(), subscription.GetName())
		assertions.NoError(err, "could not lookup subscription %s", subscription.GetName())
		assertions.True(ok, "subscription %s not found in map", subscription.GetName())

		obj := getMapItem(assertions, hzMap, subscription.GetName())
		assertions.Equal("true", obj.GetLabels()["hazelcast_test"], "subscription %s not updated in map", subscription.GetName())
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
	defer test.LogRecorder.Reset()

	hazelcastStore.Shutdown()
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel, zerolog.WarnLevel), "shutdown produces errors and/or warnings")
}

func createFakeDynamicClient() dynamic.Interface {
	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	var scheme = runtime.NewScheme()
	return fake.NewSimpleDynamicClient(scheme, subscriptions[0], subscriptions[1])
}

func TestHazelcastStore_HandleClientEvents(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Reset state
	hazelcastStore.connected.Store(false)
	hazelcastStore.reconciliations = sync.Map{}

	// Simulate connected event
	hazelcastStore.handleClientEvents(hazelcast.LifecycleStateChanged{State: hazelcast.LifecycleStateConnected})
	assertions.True(hazelcastStore.connected.Load(), "connected should be true after connected event")

	// Simulate disconnected event
	hazelcastStore.handleClientEvents(hazelcast.LifecycleStateChanged{State: hazelcast.LifecycleStateDisconnected})
	assertions.False(hazelcastStore.connected.Load(), "connected should be false after disconnected event")

	// Ensure no error logs
	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "unexpected errors have been logged")
}

// Test to cover reconciliation iteration in onConnected
func TestHazelcastStore_OnConnected(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Reset state
	hazelcastStore.connected.Store(false)
	hazelcastStore.reconciliations = sync.Map{}

	// Store a real Reconciliation object for the resource
	recon := reconciliation.NewReconciliation(
		createFakeDynamicClient(),
		&config.Current.Resources[0],
	)
	cacheName := config.Current.Resources[0].GetCacheName()
	hazelcastStore.reconciliations.Store(cacheName, recon)

	// Trigger onConnected should iterate and run reconciliation
	hazelcastStore.onConnected()
	assertions.True(hazelcastStore.connected.Load(), "connected should be true after onConnected with entry")

	// Second call should keep reconOnce true and skip reconciliation
	hazelcastStore.onConnected()
	assertions.True(hazelcastStore.connected.Load(), "connected should still be true after second onConnected")

	// Verify that reconciliation attempted and logged an error (due to fake client list error)
	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Greater(errorCount, 0, "expected error logs from reconciliation attempt")
}

// Test to cover the reset of reconOnce in onDisconnected
func TestHazelcastStore_OnDisconnected(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Reset state
	hazelcastStore.reconciliations = sync.Map{}

	// Case: reconOnce false remains false
	hazelcastStore.connected.Store(false)
	hazelcastStore.onDisconnected()
	assertions.False(hazelcastStore.connected.Load(), "connected should remain false after onDisconnected without prior connect")

	// Case: reconOnce true resets to false
	hazelcastStore.connected.Store(true)
	hazelcastStore.onDisconnected()
	assertions.False(hazelcastStore.connected.Load(), "connected should be false after onDisconnected resets flag")

	// Ensure no error logs
	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "unexpected errors have been logged")
}

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
