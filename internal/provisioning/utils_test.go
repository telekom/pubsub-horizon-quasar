// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/test"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGetDatasetForGvr(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	tests := []struct {
		name     string
		gvr      schema.GroupVersionResource
		expected string
	}{
		{
			name: "standard GVR",
			gvr: schema.GroupVersionResource{
				Group:    "subscriber.horizon.telekom.de",
				Version:  "v1",
				Resource: "subscriptions",
			},
			expected: "subscriptions.subscriber.horizon.telekom.de.v1",
		},
		{
			name: "core group (empty group)",
			gvr: schema.GroupVersionResource{
				Group:    "",
				Version:  "v1",
				Resource: "pods",
			},
			expected: "pods..v1",
		},
		{
			name: "custom resource",
			gvr: schema.GroupVersionResource{
				Group:    "mygroup.example.com",
				Version:  "v1beta1",
				Resource: "myresources",
			},
			expected: "myresources.mygroup.example.com.v1beta1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getStoreNameForGvr(tt.gvr)
			assertions.Equal(tt.expected, result)
		})
	}

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged")
}

func TestGetMongoAndHazelcastStores(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("mongo primary and hazelcast secondary", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-mongo-primary",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)

		assertions.NotNil(mongoStore, "mongo store should be identified")
		assertions.NotNil(hazelcastStore, "hazelcast store should be identified")
		assertions.IsType(&store.MongoStore{}, mongoStore)
		assertions.IsType(&store.HazelcastStore{}, hazelcastStore)
	})

	t.Run("hazelcast primary and mongo secondary", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-hazelcast-primary",
			"hazelcast",
			"mongo",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)

		assertions.NotNil(mongoStore, "mongo store should be identified")
		assertions.NotNil(hazelcastStore, "hazelcast store should be identified")
		assertions.IsType(&store.MongoStore{}, mongoStore)
		assertions.IsType(&store.HazelcastStore{}, hazelcastStore)
	})

	t.Run("only mongo primary", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-only-mongo",
			"mongo",
			"",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)

		assertions.NotNil(mongoStore, "mongo store should be identified")
		assertions.Nil(hazelcastStore, "hazelcast store should be nil")
	})

	t.Run("only hazelcast primary", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-only-hazelcast",
			"hazelcast",
			"",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)

		assertions.Nil(mongoStore, "mongo store should be nil")
		assertions.NotNil(hazelcastStore, "hazelcast store should be identified")
	})

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged")
}

func TestLogStoreIdentification(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	t.Run("both stores present", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-both-stores",
			"mongo",
			"hazelcast",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)

		// Reset log recorder before calling the function
		test.LogRecorder.Reset()

		assertions.NotPanics(func() {
			logStoreIdentification(mongoStore, hazelcastStore)
		})

		// Should log debug messages for both stores
		debugCount := test.LogRecorder.GetRecordCount(zerolog.DebugLevel)
		assertions.GreaterOrEqual(debugCount, 2, "should log debug messages for both stores")
	})

	t.Run("only mongo store", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-only-mongo-log",
			"mongo",
			"",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)

		// Reset log recorder before calling the function
		test.LogRecorder.Reset()

		assertions.NotPanics(func() {
			logStoreIdentification(mongoStore, hazelcastStore)
		})

		// Should log warning for missing hazelcast store
		warnCount := test.LogRecorder.GetRecordCount(zerolog.WarnLevel)
		assertions.GreaterOrEqual(warnCount, 1, "should log warning for missing hazelcast store")
	})

	t.Run("only hazelcast store", func(t *testing.T) {
		dualStore, err := store.SetupDualStoreManager(
			"test-only-hazelcast-log",
			"hazelcast",
			"",
		)
		assertions.NoError(err)
		defer dualStore.Shutdown()

		mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)

		// Reset log recorder before calling the function
		test.LogRecorder.Reset()

		assertions.NotPanics(func() {
			logStoreIdentification(mongoStore, hazelcastStore)
		})

		// Should log warning for missing mongo store
		warnCount := test.LogRecorder.GetRecordCount(zerolog.WarnLevel)
		assertions.GreaterOrEqual(warnCount, 1, "should log warning for missing mongo store")
	})

	t.Run("neither store present", func(t *testing.T) {
		// Reset log recorder before calling the function
		test.LogRecorder.Reset()

		assertions.NotPanics(func() {
			logStoreIdentification(nil, nil)
		})

		// Should log warnings for both missing stores
		warnCount := test.LogRecorder.GetRecordCount(zerolog.WarnLevel)
		assertions.GreaterOrEqual(warnCount, 2, "should log warnings for both missing stores")
	})
}
