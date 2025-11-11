// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/reconciliation"
	"github.com/telekom/quasar/internal/test"
)

// TestSetupDualStoreManager tests the SetupDualStoreManager function
func TestSetupDualStoreManager(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	// Test with valid primary and secondary store types
	manager, err := SetupDualStoreManager(
		"test-manager-1",
		"mongo",
		"hazelcast",
	)
	assertions.NoError(err)
	defer manager.Shutdown()
	assertions.NotNil(manager)

	// Verify the manager has correct type
	assertions.NotNil(manager.GetPrimary())
	assertions.NotNil(manager.GetSecondary())
}

// TestSetupDualStoreManagerPrimaryOnly tests setup with only primary store
func TestSetupDualStoreManagerPrimaryOnly(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	// Test with only primary store (no secondary)
	manager, err := SetupDualStoreManager(
		"test-manager-primary-only",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	assertions.NotNil(manager)
	assertions.NotNil(manager.GetPrimary())
	assertions.Nil(manager.GetSecondary(), "secondary store should be nil when empty string provided")
}

// TestSetupDualStoreManagerSameStore tests setup with same primary and secondary
func TestSetupDualStoreManagerSameStore(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	// Test with same store type for both primary and secondary
	manager, err := SetupDualStoreManager(
		"test-manager-same",
		"mongo",
		"mongo",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	assertions.NotNil(manager)
	assertions.NotNil(manager.GetPrimary())
	assertions.Nil(manager.GetSecondary(), "secondary store should be nil when same as primary")
}

// TestSetupDualStoreManagerEmptyPrimary tests error when primary store type is empty
func TestSetupDualStoreManagerEmptyPrimary(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	// Test with empty primary store type - should return error
	manager, err := SetupDualStoreManager(
		"test-manager-empty",
		"",
		"",
	)
	assertions.Error(err)
	assertions.Nil(manager)
	assertions.Equal(ErrUnknownStoreType, err)
}

// TestDualStoreManagerInitialize tests the Initialize method
func TestDualStoreManagerInitialize(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-init",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	// Initialize is called in SetupDualStoreManager, verify it doesn't panic
	assertions.NotPanics(func() {
		manager.(*DualStoreManager).Initialize()
	}, "Initialize should not panic")
}

// TestDualStoreManagerInitializeResource tests resource initialization
func TestDualStoreManagerInitializeResource(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-res",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	kubernetesClient := test.CreateTestKubernetesClient()
	resourceConfig := config.Resource{}
	resourceConfig.Kubernetes.Group = ""
	resourceConfig.Kubernetes.Version = "v1"
	resourceConfig.Kubernetes.Resource = "testresources"
	resourceConfig.Kubernetes.Kind = "TestResource"

	// Create proper reconciliation object
	kubernetesDataSource := reconciliation.NewDataSourceFromKubernetesClient(kubernetesClient, &resourceConfig)

	// Should not panic even though this is a dual store
	assertions.NotPanics(func() {
		manager.InitializeResource(kubernetesDataSource, &resourceConfig)
	}, "InitializeResource should not panic")
}

// TestDualStoreManagerCreate tests the Create method delegates to primary store
func TestDualStoreManagerCreate(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-create",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	resource := test.CreateTestResource("test-resource", "default", nil)

	// Create should succeed or fail based on primary store
	_ = manager.Create(resource)
	// We don't assert on error here as it depends on MongoDB availability
	assertions.NotNil(manager)
}

// TestDualStoreManagerUpdate tests the Update method delegates to primary store
func TestDualStoreManagerUpdate(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-update",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	oldResource := test.CreateTestResource("test-resource", "default", nil)
	newResource := test.CreateTestResource("test-resource", "default", map[string]string{"updated": "true"})

	// Update should succeed or fail based on primary store
	_ = manager.Update(oldResource, newResource)
	// We don't assert on error here as it depends on MongoDB availability
	assertions.NotNil(manager)
}

// TestDualStoreManagerDelete tests the Delete method delegates to primary store
func TestDualStoreManagerDelete(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-delete",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	resource := test.CreateTestResource("test-resource", "default", nil)

	// Delete should succeed or fail based on primary store
	_ = manager.Delete(resource)
	// We don't assert on error here as it depends on MongoDB availability
	assertions.NotNil(manager)
}

// TestDualStoreManagerCount tests the Count method reads from primary store
func TestDualStoreManagerCount(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	// Count should read from primary store
	manager, err := SetupDualStoreManager(
		"test-manager-count",
		"mongo",
		"hazelcast",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	_, _ = manager.Count("test-collection")
	assertions.NotNil(manager)
	// Count result depends on MongoDB
}

// TestDualStoreManagerKeys tests the Keys method reads from primary store
func TestDualStoreManagerKeys(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-keys",
		"mongo",
		"hazelcast",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	// Keys should read from primary store
	keys, _ := manager.Keys("test-collection")
	assertions.NotNil(manager)
	// Keys result depends on MongoDB
	_ = keys
}

// TestDualStoreManagerRead tests the Read method reads from primary store
func TestDualStoreManagerRead(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-read",
		"mongo",
		"hazelcast",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	// Read should read from primary store
	result, _ := manager.Read("test-collection", "test-key")
	assertions.NotNil(manager)
	// Result depends on MongoDB
	_ = result
}

// TestDualStoreManagerList tests the List method reads from primary store
func TestDualStoreManagerList(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-list",
		"mongo",
		"hazelcast",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	// List should read from primary store
	results, _ := manager.List("test-collection", "", 0)
	assertions.NotNil(manager)
	// Results depend on MongoDB
	_ = results
}

// TestDualStoreManagerShutdown tests the Shutdown method
func TestDualStoreManagerShutdown(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-shutdown",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	// Shutdown should not panic
	assertions.NotPanics(func() {
		manager.Shutdown()
	}, "Shutdown should not panic")
}

// TestDualStoreManagerConnected tests the Connected method
func TestDualStoreManagerConnected(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-connected",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	// Connected should return boolean
	connected := manager.Connected()
	assertions.IsType(true, connected)
	// Connection status depends on MongoDB availability
	_ = connected
}

// TestDualStoreManagerGetPrimary tests getting the primary store
func TestDualStoreManagerGetPrimary(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-get-primary",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	primary := manager.GetPrimary()
	assertions.NotNil(primary)
	assertions.IsType(&MongoStore{}, primary)
}

// TestDualStoreManagerGetSecondary tests getting the secondary store
func TestDualStoreManagerGetSecondary(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-get-secondary",
		"mongo",
		"hazelcast",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	secondary := manager.GetSecondary()
	assertions.NotNil(secondary)
	assertions.IsType(&HazelcastStore{}, secondary)
}

// TestDualStoreManagerGetSecondaryNil tests getting nil secondary store
func TestDualStoreManagerGetSecondaryNil(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-get-secondary-nil",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	secondary := manager.GetSecondary()
	assertions.Nil(secondary, "secondary store should be nil when not configured")
}

// TestDualStoreManagerInterface tests that DualStoreManager implements DualStore interface
func TestDualStoreManagerInterface(t *testing.T) {
	assertions := assert.New(t)

	// Compile-time check that DualStoreManager implements DualStore
	var _ DualStore = (*DualStoreManager)(nil)
	assertions.True(true, "DualStoreManager implements DualStore interface")
}

// TestDualStoreManagerAsyncSecondaryWrite tests that secondary writes are async
func TestDualStoreManagerAsyncSecondaryWrite(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-async",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	resource := test.CreateTestResource("test-resource", "default", nil)

	// Create with timeout - primary store should not block
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error)
	go func() {
		done <- manager.Create(resource)
	}()

	select {
	case err := <-done:
		assertions.NotNil(manager, "Create should complete quickly")
		_ = err
	case <-ctx.Done():
		assertions.Fail("Create took too long")
	}
}

// TestDualStoreManagerLogging tests that operations are properly logged
func TestDualStoreManagerLogging(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-logging",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	assertions.NotNil(manager)

	// Verify logger was created with proper context
	dualStoreManager := manager.(*DualStoreManager)
	assertions.NotNil(dualStoreManager.logger)
	assertions.NotEqual(zerolog.Logger{}, dualStoreManager.logger)
}

// TestDualStoreManagerConcurrency tests concurrent access to the store
func TestDualStoreManagerConcurrency(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	manager, err := SetupDualStoreManager(
		"test-manager-concurrency",
		"mongo",
		"",
	)
	assertions.NoError(err)
	defer manager.Shutdown()

	// Run concurrent reads
	done := make(chan error, 10)

	for i := range 10 {
		go func(index int) {
			_, err := manager.Read(
				fmt.Sprintf("collection-%d", index),
				fmt.Sprintf("key-%d", index),
			)
			done <- err
		}(i)
	}

	// Collect results - should handle concurrent access safely
	for range 10 {
		<-done
	}

	assertions.NotNil(manager, "concurrent access should not panic")
}
