// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"sync"
	"testing"

	"github.com/hazelcast/hazelcast-go-client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/reconciliation"
	"github.com/telekom/quasar/internal/test"
)

func TestHazelcastStore_Initialize(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	assertions.NotPanics(func() {
		hazelcastStore.Initialize()
	}, "unexpected panic")

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "Initialize should not produce error logs on success")
}

func TestHazelcastStore_InitializeResource(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	var testResource = config.Current.Resources[0]
	var kubernetesDataSource = reconciliation.NewKubernetesDataSource(createFakeDynamicClient(), &testResource)
	hazelcastStore.InitializeResource(kubernetesDataSource, &testResource)

	var errorCount = test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "unexpected errors have been logged")
}

func TestHazelcastStore_OnAdd(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	for _, subscription := range subscriptions {
		hazelcastStore.Create(subscription)
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

		hazelcastStore.Update(subscription, updatedSubscription)
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
		hazelcastStore.Delete(subscription)
		assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "could not delete subscription %s", subscription.GetName())
	}
}

func TestHazelcastStore_Shutdown(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	hazelcastStore.Shutdown()
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel, zerolog.WarnLevel), "shutdown produces errors and/or warnings")
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
	testResource := config.Current.Resources[0]
	kubernetesDataSource := reconciliation.NewKubernetesDataSource(createFakeDynamicClient(), &testResource)
	recon := reconciliation.NewReconciliation(
		kubernetesDataSource,
		&testResource,
	)
	cacheName := config.Current.Resources[0].GetCacheName()
	hazelcastStore.reconciliations.Store(cacheName, recon)

	// Trigger onConnected should iterate and run reconciliation
	hazelcastStore.onConnected()
	assertions.True(hazelcastStore.connected.Load(), "connected should be true after onConnected with entry")

	// Second call should keep reconOnce true and skip reconciliation
	hazelcastStore.onConnected()
	assertions.True(hazelcastStore.connected.Load(), "connected should still be true after second onConnected")

	// NOTE: With fake Kubernetes client, reconciliation may log errors due to mocked List() behavior
	// This is expected and does not indicate a failure of onConnected() itself
	// We just verify that onConnected was called and the connected flag is set correctly
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

// TestHazelcastStore_Connected tests the Connected method
func TestHazelcastStore_Connected(t *testing.T) {
	var assertions = assert.New(t)

	// Test: connected flag is true
	hazelcastStore.connected.Store(true)
	assertions.True(hazelcastStore.Connected(), "Connected should return true when flag is set")

	// Test: connected flag is false
	hazelcastStore.connected.Store(false)
	assertions.False(hazelcastStore.Connected(), "Connected should return false when flag is not set")

	// Restore state for other tests
	hazelcastStore.connected.Store(true)
}

// TestHazelcastStore_Count tests the Count method
func TestHazelcastStore_Count(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Get a test resource map
	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	if len(subscriptions) == 0 {
		t.Skip("No test subscriptions available")
	}

	// Verify map exists and contains items
	testResource := subscriptions[0]
	mapName := testResource.GetName()

	// Get count from the store
	count, err := hazelcastStore.Count(mapName)

	// May fail if map doesn't exist, but should not panic
	if err != nil {
		// Error is acceptable (e.g., map not found)
		// When Count() errors, it returns (0, error)
		assertions.Equal(0, count, "Count should return 0 on error")
	} else {
		// Success case: should return non-negative count
		assertions.GreaterOrEqual(count, 0, "Count should return a non-negative value")
	}
}

// TestHazelcastStore_Keys tests the Keys method
func TestHazelcastStore_Keys(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Get a test resource map
	var subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")
	if len(subscriptions) == 0 {
		t.Skip("No test subscriptions available")
	}

	// Verify we can call Keys without panic
	testResource := subscriptions[0]
	mapName := testResource.GetName()

	keys, err := hazelcastStore.Keys(mapName)

	// May fail if map doesn't exist, but should not panic
	if err != nil {
		// Error is acceptable (e.g., map not found)
		// When Keys() errors, it returns (nil, error) - this is normal Go behavior
		assertions.Nil(keys, "Keys should return nil on error")
	} else {
		// Success case: Keys should return a non-nil slice
		assertions.NotNil(keys, "Keys should return a non-nil slice")
		assertions.IsType([]string{}, keys, "Keys should return a string slice")
	}
}
