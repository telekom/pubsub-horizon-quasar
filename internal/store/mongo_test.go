// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/reconciliation"
	"github.com/telekom/quasar/internal/test"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Global MongoDB instance for tests
var mongoStore *MongoStore

// setupMongoStore initializes the MongoDB store for tests
func setupMongoStore() *MongoStore {
	if mongoStore == nil || !mongoStore.Connected() {
		if config.Current.Store.Mongo.Uri == "" {
			mongoHost := test.EnvOrDefault("MONGO_HOST", "localhost")
			mongoPort := test.EnvOrDefault("MONGO_PORT", "27017")
			config.Current.Store.Mongo.Uri = fmt.Sprintf("mongodb://%s:%s", mongoHost, mongoPort)
			config.Current.Store.Mongo.Database = config.Current.Fallback.Mongo.Database
			if config.Current.Store.Mongo.Database == "" {
				config.Current.Store.Mongo.Database = "test_db"
			}
		}

		// Add configuration for TestResource
		var foundTestResourceConfig bool
		for _, res := range config.Current.Resources {
			if res.Kubernetes.Kind == "TestResource" && res.Kubernetes.Version == "v1" {
				foundTestResourceConfig = true
				break
			}
		}

		if !foundTestResourceConfig {
			testResourceConfig := config.Resource{}
			testResourceConfig.Kubernetes.Group = "" // Empty group for core v1
			testResourceConfig.Kubernetes.Version = "v1"
			testResourceConfig.Kubernetes.Resource = "testresources"
			testResourceConfig.Kubernetes.Kind = "TestResource"
			// No MongoId field configured to use UID as ID
			config.Current.Resources = append(config.Current.Resources, testResourceConfig)
		}

		mongoStore = new(MongoStore)
		mongoStore.Initialize()
	}
	return mongoStore
}

// getTestCollectionName returns the name of the collection for test resources
func getTestCollectionName() string {
	return "testresources..v1"
}

// cleanupMongoCollection deletes test data from the collection
func cleanupMongoCollection() {
	if mongoStore != nil && mongoStore.Connected() {
		mongoStore.client.Database(config.Current.Store.Mongo.Database).Collection(getTestCollectionName()).Drop(context.Background())
	}
}

// TestMongoStore_CreateFilter tests the createFilter method functionality
func TestMongoStore_CreateFilter(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()

	// Test with valid object
	resource := test.CreateTestResource("test-resource", "default", nil)
	filter, err := store.createFilter(resource)

	assertions.NoError(err)
	assertions.Equal(bson.M{"_id": "default/test-resource"}, filter)

	// Test with object without namespace
	resourceNoNs := test.CreateTestResource("test-resource", "", nil)
	filter, err = store.createFilter(resourceNoNs)

	assertions.NoError(err)
	assertions.Equal(bson.M{"_id": "test-resource"}, filter)
}

// TestMongoStore_Create tests the Create method functionality
func TestMongoStore_Create(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	resource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "test"})

	err := store.Create(resource)
	assertions.NoError(err)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(getTestCollectionName())
	filter := bson.M{"_id": "default/test-resource"}

	var result bson.M
	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.NoError(err)

	assertions.Equal("test-resource", result["metadata"].(bson.M)["name"])
	assertions.Equal("default", result["metadata"].(bson.M)["namespace"])
	assertions.Equal("test", result["metadata"].(bson.M)["labels"].(bson.M)["app"])

	resource.SetLabels(map[string]string{"app": "updated"})
	err = store.Create(resource)
	assertions.NoError(err)

	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.NoError(err)
	assertions.Equal("updated", result["metadata"].(bson.M)["labels"].(bson.M)["app"])

	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_Update tests the Update method functionality
func TestMongoStore_Update(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	oldResource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "test"})

	err := store.Create(oldResource)
	assertions.NoError(err)

	newResource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "updated"})
	newResource.Object["spec"] = map[string]interface{}{
		"replicas": 3,
	}

	err = store.Update(oldResource, newResource)
	assertions.NoError(err)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(getTestCollectionName())
	filter := bson.M{"_id": "default/test-resource"}

	var result bson.M
	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.NoError(err)

	assertions.Equal("updated", result["metadata"].(bson.M)["labels"].(bson.M)["app"])
	assertions.Equal(int32(3), result["spec"].(bson.M)["replicas"])
}

// TestMongoStore_Delete tests the Delete method functionality
func TestMongoStore_Delete(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	resource := test.CreateTestResource("test-resource", "default", nil)

	err := store.Create(resource)
	assertions.NoError(err)

	err = store.Delete(resource)
	assertions.NoError(err)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(getTestCollectionName())
	filter := bson.M{"_id": "default/test-resource"}

	var result bson.M
	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.Equal(mongo.ErrNoDocuments, err, "document should no longer exist")
}

// TestMongoStore_Count tests the Count method functionality
func TestMongoStore_Count(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	count, err := store.Count(getTestCollectionName())
	assertions.NoError(err)
	assertions.Equal(0, count)

	// Add three resources
	for i := 1; i <= 3; i++ {
		resource := test.CreateTestResource(fmt.Sprintf("test-resource-%d", i), "default", nil)
		err = store.Create(resource)
		assertions.NoError(err)
	}

	count, err = store.Count(getTestCollectionName())
	assertions.NoError(err)
	assertions.Equal(3, count)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_Keys tests the Keys method functionality
func TestMongoStore_Keys(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	keys, err := store.Keys(getTestCollectionName())
	assertions.NoError(err)
	assertions.Empty(keys)

	// Add three resources
	expectedKeys := []string{
		"default/test-resource-1",
		"default/test-resource-2",
		"default/test-resource-3",
	}

	for i := 1; i <= 3; i++ {
		resource := test.CreateTestResource(fmt.Sprintf("test-resource-%d", i), "default", nil)
		err = store.Create(resource)
		assertions.NoError(err)
	}

	keys, err = store.Keys(getTestCollectionName())
	assertions.NoError(err)
	assertions.ElementsMatch(expectedKeys, keys)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_Read tests the Read method functionality
func TestMongoStore_Read(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	resource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "test"})
	resource.Object["spec"] = map[string]interface{}{
		"replicas": 2,
	}

	err := store.Create(resource)
	assertions.NoError(err)

	result, err := store.Read(getTestCollectionName(), "default/test-resource")
	assertions.NoError(err)
	assertions.NotNil(result)

	assertions.Equal("test-resource", result.GetName())
	assertions.Equal("default", result.GetNamespace())
	assertions.Equal("test", result.GetLabels()["app"])

	result, err = store.Read(getTestCollectionName(), "non-existent")
	assertions.NoError(err)
	assertions.Nil(result)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_List tests the List method functionality
func TestMongoStore_List(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	labels := []map[string]string{
		{"app": "frontend", "env": "prod"},
		{"app": "backend", "env": "prod"},
		{"app": "frontend", "env": "dev"},
	}

	for i, label := range labels {
		resource := test.CreateTestResource(fmt.Sprintf("test-resource-%d", i+1), "default", label)
		err := store.Create(resource)
		assertions.NoError(err)
	}

	results, err := store.List(getTestCollectionName(), "", 0)
	assertions.NoError(err)
	assertions.Len(results, 3)

	results, err = store.List(getTestCollectionName(), "metadata.labels.app=frontend", 0)
	assertions.NoError(err)
	assertions.Len(results, 2)

	results, err = store.List(getTestCollectionName(), "metadata.labels.env=prod", 1)
	assertions.NoError(err)
	assertions.Len(results, 1)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_ParseFieldSelector tests the parseFieldSelector method
func TestMongoStore_ParseFieldSelector(t *testing.T) {
	store := &MongoStore{}

	tests := []struct {
		name           string
		fieldSelector  string
		expectedFilter bson.M
	}{
		{
			name:           "empty selector",
			fieldSelector:  "",
			expectedFilter: bson.M{},
		},
		{
			name:          "single field selector",
			fieldSelector: "metadata.name=test-resource",
			expectedFilter: bson.M{
				"metadata.name": "test-resource",
			},
		},
		{
			name:          "multiple field selectors",
			fieldSelector: "metadata.name=test-resource,metadata.namespace=default",
			expectedFilter: bson.M{
				"metadata.name":      "test-resource",
				"metadata.namespace": "default",
			},
		},
		{
			name:          "selector with whitespace",
			fieldSelector: " metadata.name = test-resource , metadata.namespace = default ",
			expectedFilter: bson.M{
				"metadata.name":      "test-resource",
				"metadata.namespace": "default",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.parseFieldSelector(tt.fieldSelector)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedFilter, result)
		})
	}
}

// TestMongoStore_InitializeShutdown tests Initialize and Shutdown methods
func TestMongoStore_InitializeShutdown(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := new(MongoStore)
	assertions.NotPanics(func() {
		store.Initialize()
	}, "no panic expected")

	assertions.True(store.Connected())
	assertions.NotNil(store.client)

	assertions.NotPanics(func() {
		store.Shutdown()
	}, "no panic expected")

	assertions.False(store.Connected())
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_InitializeResource tests the InitializeResource method functionality
func TestMongoStore_InitializeResource(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()

	resourceConfig := config.Resource{}
	resourceConfig.Kubernetes.Group = "" // Empty group for core v1
	resourceConfig.Kubernetes.Version = "v1"
	resourceConfig.Kubernetes.Resource = "testresources"
	resourceConfig.Kubernetes.Kind = "TestResource"

	// Add a test index configuration (MongoResourceIndex is map[string]int where value is the index type)
	// 1 = ascending, -1 = descending
	indexConfig := config.MongoResourceIndex{
		"metadata.name": 1, // Ascending index on metadata.name
	}
	resourceConfig.MongoIndexes = []config.MongoResourceIndex{indexConfig}

	kubernetesClient := test.CreateTestKubernetesClient()
	kubernetesDataSource := reconciliation.NewDataSourceFromKubernetesClient(kubernetesClient, &resourceConfig)
	assertions.NotPanics(func() {
		store.InitializeResource(kubernetesDataSource, &resourceConfig)
	}, "no panic expected during resource initialization")

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(resourceConfig.GetDataSet())
	indexCursor, err := collection.Indexes().List(context.Background())
	assertions.NoError(err)

	indexes := make([]bson.M, 0)
	err = indexCursor.All(context.Background(), &indexes)
	assertions.NoError(err)
	assertions.GreaterOrEqual(len(indexes), 1, "at least one index should exist")

	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_ParseFieldSelectorEdgeCases tests edge cases in parseFieldSelector method
func TestMongoStore_ParseFieldSelectorEdgeCases(t *testing.T) {
	store := &MongoStore{}

	// Test selector with invalid format
	filter, err := store.parseFieldSelector("invalid-format")
	assert.NoError(t, err)
	assert.Empty(t, filter)

	// Test selector with empty right side
	filter, err = store.parseFieldSelector("metadata.name=")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": ""}, filter)

	// Test selector with multiple equals signs
	filter, err = store.parseFieldSelector("metadata.name=value=with=equals")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": "value=with=equals"}, filter)

	// Test selector with whitespace in values
	filter, err = store.parseFieldSelector("metadata.name=name with spaces")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": "name with spaces"}, filter)

	// Test selector with special characters
	filter, err = store.parseFieldSelector("metadata.name=special@#$%^&*chars")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": "special@#$%^&*chars"}, filter)
}

// TestMongoStore_OperationWithBadObject tests error handling with operations on invalid objects
func TestMongoStore_OperationWithBadObject(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	if testing.Short() {
		t.Skip("Skipping operation with bad object test in short mode")
	}

	store := setupMongoStore()
	cleanupMongoCollection()

	// Create incomplete object missing important metadata
	// Note: GetMongoId() uses GetUID(), which always has a value (even if empty)
	// Therefore operations will not fail, but execute successfully
	badObject := &unstructured.Unstructured{
		Object: map[string]interface{}{
			// No metadata like name or namespace
			"spec": map[string]interface{}{
				"replicas": 3,
			},
		},
	}

	err := store.Create(badObject)
	assertions.NoError(err, "Create should succeed with invalid metadata as UID is used for ID")

	err = store.Update(badObject, badObject)
	assertions.NoError(err, "Update should succeed with invalid metadata")

	err = store.Delete(badObject)
	assertions.NoError(err, "Delete should succeed with invalid metadata")

	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

// TestMongoStore_ErrorHandling tests how store implementation handles database errors
func TestMongoStore_ErrorHandling(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()

	// Test non-existent collection (should not throw errors)
	count, err := store.Count("non_existent_collection")
	assertions.NoError(err)
	assertions.Equal(0, count)

	// Test Keys on non-existent collection
	keys, err := store.Keys("non_existent_collection")
	assertions.NoError(err)
	assertions.Empty(keys)

	// Test Read with invalid key format (should not throw errors)
	result, err := store.Read(getTestCollectionName(), "")
	assertions.NoError(err)
	assertions.Nil(result)

	// Test List with non-existent collection
	results, err := store.List("non_existent_collection", "", 0)
	assertions.NoError(err)

	// List returns empty slice for empty collection, not nil
	if results != nil {
		assertions.Empty(results)
	}

	// Test List with invalid field selector
	results, err = store.List(getTestCollectionName(), "invalid-selector", 0)
	assertions.NoError(err)

	// Should return empty slice if selector is invalid and collection is empty
	if results != nil {
		assertions.Empty(results)
	}
}
