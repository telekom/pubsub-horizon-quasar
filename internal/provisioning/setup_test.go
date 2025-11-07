// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"os"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/test"
)

var testProvisioningStore store.DualStore

// TestMain sets up the test environment for all provisioning tests.
// This is a package-level setup that runs before all tests in this package.
func TestMain(m *testing.M) {
	// Setup Docker containers for MongoDB and Hazelcast
	test.SetupDocker(&test.Options{
		MongoDb:   true,
		Hazelcast: true,
	})

	// Build test configuration
	config.Current = buildTestConfig()

	// Install log recorder to capture log output in tests
	test.InstallLogRecorder()

	// Initialize logger for provisioning package
	logger = createTestLogger()

	// Setup provisioning API store for tests
	var err error
	testProvisioningStore, err = store.SetupDualStoreManager(
		"TestProvisioningStore",
		"mongo",
		"hazelcast",
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to setup test provisioning store")
	}

	provisioningApiStore = testProvisioningStore

	// Run all tests in this package
	code := m.Run()

	// Cleanup
	if testProvisioningStore != nil {
		testProvisioningStore.Shutdown()
	}
	test.TeardownDocker()
	os.Exit(code)
}

func buildTestConfig() *config.Configuration {
	// Use shared base configuration (MongoDB + Hazelcast)
	testConfig := test.BuildBaseTestConfig()

	// Add provisioning-specific configuration
	testConfig.Provisioning.LogLevel = "debug"
	testConfig.Provisioning.Security.Enabled = false
	testConfig.Provisioning.Store.Primary.Type = "mongo"
	testConfig.Provisioning.Store.Secondary.Type = "hazelcast"

	// Add test resource for provisioning tests
	test.AddTestResource(
		testConfig,
		"subscriber.horizon.telekom.de", // group
		"v1",                            // version
		"subscriptions",                 // resource
		"Subscription",                  // kind
		"default",                       // namespace
	)

	return testConfig
}

func createTestLogger() *zerolog.Logger {
	testLogger := log.Logger.With().Str("logger", "provisioning-test").Logger()
	testLogger = testLogger.Level(zerolog.DebugLevel)
	return &testLogger
}
