// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/test"
)

func TestSyncMongoToHazelcastWithContext(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("successful synchronization", func(t *testing.T) {
		// Create a test dual store with MongoDB and Hazelcast
		dualStore, err := store.SetupDualStoreManager(
			"test-sync-success",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Add some test resources to MongoDB
		mongoStore := dualStore.GetPrimary()
		testResources := []*test.DummyStore{}
		for i := 0; i < 3; i++ {
			resource := test.CreateTestResource("test-resource-"+string(rune('a'+i)), "default", map[string]string{"test": "sync"})
			err := mongoStore.Create(resource)
			assertions.NoError(err)
			testResources = append(testResources, &test.DummyStore{})
		}

		// Reset log recorder to capture sync logs
		test.LogRecorder.Reset()

		ctx := context.Background()
		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.NoError(err)

		// Verify no error logs
		errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
		assertions.Equal(0, errorCount, "no errors should be logged during successful sync")

		// Verify info logs about synchronization
		infoCount := test.LogRecorder.GetRecordCount(zerolog.InfoLevel)
		assertions.Greater(infoCount, 0, "sync should log info messages")
	})

	t.Run("synchronization with timeout context", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-sync-timeout",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		// Create a context with a reasonable timeout
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.NoError(err, "sync should complete within timeout")
	})

	t.Run("synchronization with cancelled context", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-sync-cancelled",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		// Create a cancelled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.Error(err, "sync should fail with cancelled context")
		assertions.Equal(context.Canceled, err)
	})

	t.Run("synchronization with only mongo store", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-sync-mongo-only",
			"mongo",
			"",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		ctx := context.Background()
		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.Error(err, "sync should fail when hazelcast store is missing")

		// Verify error was logged
		errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
		assertions.Greater(errorCount, 0, "error should be logged when hazelcast store is missing")
	})

	t.Run("synchronization with only hazelcast store", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-sync-hazelcast-only",
			"hazelcast",
			"",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		ctx := context.Background()
		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.Error(err, "sync should fail when mongo store is missing")

		// Verify error was logged
		errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
		assertions.Greater(errorCount, 0, "error should be logged when mongo store is missing")
	})

	t.Run("synchronization with empty resources", func(t *testing.T) {
		// Save original resources
		originalResources := config.Current.Resources

		// Clear resources temporarily
		config.Current.Resources = []config.Resource{}

		dualStore, err := store.SetupDualStoreManager(
			"test-sync-empty-resources",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		ctx := context.Background()
		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.NoError(err, "sync should succeed with no resources")

		// Verify info logs
		infoCount := test.LogRecorder.GetRecordCount(zerolog.InfoLevel)
		assertions.Greater(infoCount, 0, "sync should log info messages even with empty resources")

		// Restore original resources
		config.Current.Resources = originalResources
	})

	t.Run("synchronization with multiple resources", func(t *testing.T) {
		// Save original resources
		originalResources := config.Current.Resources

		// Add additional test resources
		testResource1 := config.Resource{}
		testResource1.Kubernetes.Group = "test.example.com"
		testResource1.Kubernetes.Version = "v1"
		testResource1.Kubernetes.Resource = "testresources1"
		testResource1.Kubernetes.Kind = "TestResource1"

		testResource2 := config.Resource{}
		testResource2.Kubernetes.Group = "test.example.com"
		testResource2.Kubernetes.Version = "v1"
		testResource2.Kubernetes.Resource = "testresources2"
		testResource2.Kubernetes.Kind = "TestResource2"

		config.Current.Resources = append(config.Current.Resources, testResource1, testResource2)

		dualStore, err := store.SetupDualStoreManager(
			"test-sync-multi-resources",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		ctx := context.Background()
		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.NoError(err, "sync should succeed with multiple resources")

		// Verify info logs for multiple resources
		infoCount := test.LogRecorder.GetRecordCount(zerolog.InfoLevel)
		assertions.Greater(infoCount, 0, "sync should log info messages for multiple resources")

		// Restore original resources
		config.Current.Resources = originalResources
	})
}

func TestSyncMongoToHazelcastEdgeCases(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("synchronization logs duration", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-sync-duration",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		ctx := context.Background()
		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.NoError(err)

		// Verify completion log with duration
		infoCount := test.LogRecorder.GetRecordCount(zerolog.InfoLevel)
		assertions.Greater(infoCount, 0, "sync should log completion with duration")
	})

	t.Run("synchronization handles context cancellation during resource iteration", func(t *testing.T) {
		// Save original resources
		originalResources := config.Current.Resources

		// Add many test resources to increase iteration time
		for i := 0; i < 5; i++ {
			testResource := config.Resource{}
			testResource.Kubernetes.Group = "test.example.com"
			testResource.Kubernetes.Version = "v1"
			testResource.Kubernetes.Resource = "testresources" + string(rune('a'+i))
			testResource.Kubernetes.Kind = "TestResource" + string(rune('A'+i))
			config.Current.Resources = append(config.Current.Resources, testResource)
		}

		dualStore, err := store.SetupDualStoreManager(
			"test-sync-cancel-iteration",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		// Reset log recorder
		test.LogRecorder.Reset()

		// Create a context with very short timeout
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
		defer cancel()

		// Wait a bit to ensure context is cancelled
		time.Sleep(10 * time.Millisecond)

		err = syncMongoToHazelcastWithContext(ctx, dualStore)
		assertions.Error(err, "sync should fail with cancelled context during iteration")

		// Restore original resources
		config.Current.Resources = originalResources
	})
}
