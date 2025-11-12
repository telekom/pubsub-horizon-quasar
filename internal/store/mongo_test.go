// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package store

import (
	"context"
	"fmt"
	"net"
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

var mongoStore *MongoStore

const testCollectionName = "testresources..v1"

func setupMongoStore() *MongoStore {
	if mongoStore != nil && mongoStore.Connected() {
		return mongoStore
	}

	if config.Current.Store.Mongo.Uri == "" {
		mongoHost := test.EnvOrDefault("MONGO_HOST", "localhost")
		mongoPort := test.EnvOrDefault("MONGO_PORT", "27017")
		config.Current.Store.Mongo.Uri = "mongodb://" + net.JoinHostPort(mongoHost, mongoPort)
		config.Current.Store.Mongo.Database = config.Current.Fallback.Mongo.Database
		if config.Current.Store.Mongo.Database == "" {
			config.Current.Store.Mongo.Database = "test_db"
		}
	}

	var foundTestResourceConfig bool
	for _, res := range config.Current.Resources {
		if res.Kubernetes.Kind == "TestResource" && res.Kubernetes.Version == "v1" {
			foundTestResourceConfig = true
			break
		}
	}

	if !foundTestResourceConfig {
		testResourceConfig := config.Resource{}
		testResourceConfig.Kubernetes.Group = ""
		testResourceConfig.Kubernetes.Version = "v1"
		testResourceConfig.Kubernetes.Resource = "testresources"
		testResourceConfig.Kubernetes.Kind = "TestResource"
		config.Current.Resources = append(config.Current.Resources, testResourceConfig)
	}

	mongoStore = new(MongoStore)
	mongoStore.Initialize()
	return mongoStore
}

func cleanupMongoCollection() {
	if mongoStore != nil && mongoStore.Connected() {
		err := mongoStore.client.Database(config.Current.Store.Mongo.Database).Collection(testCollectionName).Drop(context.Background())
		if err != nil {
			return
		}
	}
}

func TestMongoStore_CreateFilter(t *testing.T) {
	assertions := assert.New(t)
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
//
//goland:noinspection DuplicatedCode
func TestMongoStore_Create(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	resource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "test"})

	err := store.Create(resource)
	assertions.NoError(err)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(testCollectionName)
	filter := bson.M{"_id": "default/test-resource"}

	var result bson.M
	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.NoError(err)

	metadata, ok := result["metadata"].(bson.M)
	assertions.True(ok, "metadata should be a bson.M")
	assertions.Equal("test-resource", metadata["name"])
	assertions.Equal("default", metadata["namespace"])

	labels, ok := metadata["labels"].(bson.M)
	assertions.True(ok, "labels should be a bson.M")
	assertions.Equal("test", labels["app"])

	resource.SetLabels(map[string]string{"app": "updated"})
	err = store.Create(resource)
	assertions.NoError(err)

	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.NoError(err)

	metadata, ok = result["metadata"].(bson.M)
	assertions.True(ok, "metadata should be a bson.M")
	labels, ok = metadata["labels"].(bson.M)
	assertions.True(ok, "labels should be a bson.M")
	assertions.Equal("updated", labels["app"])

	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

//goland:noinspection DuplicatedCode
func TestMongoStore_Update(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	oldResource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "test"})

	err := store.Create(oldResource)
	assertions.NoError(err)

	newResource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "updated"})
	newResource.Object["spec"] = map[string]any{
		"replicas": 3,
	}

	err = store.Update(oldResource, newResource)
	assertions.NoError(err)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(testCollectionName)
	filter := bson.M{"_id": "default/test-resource"}

	var result bson.M
	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.NoError(err)

	metadata, ok := result["metadata"].(bson.M)
	assertions.True(ok, "metadata should be a bson.M")
	labels, ok := metadata["labels"].(bson.M)
	assertions.True(ok, "labels should be a bson.M")
	assertions.Equal("updated", labels["app"])

	spec, ok := result["spec"].(bson.M)
	assertions.True(ok, "spec should be a bson.M")
	assertions.Equal(int32(3), spec["replicas"])
}

func TestMongoStore_Delete(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	resource := test.CreateTestResource("test-resource", "default", nil)

	err := store.Create(resource)
	assertions.NoError(err)

	err = store.Delete(resource)
	assertions.NoError(err)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(testCollectionName)
	filter := bson.M{"_id": "default/test-resource"}

	var result bson.M
	err = collection.FindOne(context.Background(), filter).Decode(&result)
	assertions.Equal(mongo.ErrNoDocuments, err, "document should no longer exist")
}

func TestMongoStore_Count(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	count, err := store.Count(testCollectionName)
	assertions.NoError(err)
	assertions.Equal(0, count)

	for i := 1; i <= 3; i++ {
		resource := test.CreateTestResource(fmt.Sprintf("test-resource-%d", i), "default", nil)
		err = store.Create(resource)
		assertions.NoError(err)
	}

	count, err = store.Count(testCollectionName)
	assertions.NoError(err)
	assertions.Equal(3, count)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

func TestMongoStore_Keys(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	keys, err := store.Keys(testCollectionName)
	assertions.NoError(err)
	assertions.Empty(keys)

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

	keys, err = store.Keys(testCollectionName)
	assertions.NoError(err)
	assertions.ElementsMatch(expectedKeys, keys)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

func TestMongoStore_Read(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()
	cleanupMongoCollection()

	resource := test.CreateTestResource("test-resource", "default", map[string]string{"app": "test"})
	resource.Object["spec"] = map[string]any{
		"replicas": 2,
	}

	err := store.Create(resource)
	assertions.NoError(err)

	result, err := store.Read(testCollectionName, "default/test-resource")
	assertions.NoError(err)
	assertions.NotNil(result)

	assertions.Equal("test-resource", result.GetName())
	assertions.Equal("default", result.GetNamespace())
	assertions.Equal("test", result.GetLabels()["app"])

	result, err = store.Read(testCollectionName, "non-existent")
	assertions.ErrorIs(err, ErrResourceNotFound)
	assertions.Nil(result)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

func TestMongoStore_List(t *testing.T) {
	assertions := assert.New(t)
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

	results, err := store.List(testCollectionName, "", 0)
	assertions.NoError(err)
	assertions.Len(results, 3)

	results, err = store.List(testCollectionName, "metadata.labels.app=frontend", 0)
	assertions.NoError(err)
	assertions.Len(results, 2)

	results, err = store.List(testCollectionName, "metadata.labels.env=prod", 1)
	assertions.NoError(err)
	assertions.Len(results, 1)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

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

func TestMongoStore_InitializeShutdown(t *testing.T) {
	assertions := assert.New(t)
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

func TestMongoStore_InitializeResource(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()

	resourceConfig := config.Resource{}
	resourceConfig.Kubernetes.Group = ""
	resourceConfig.Kubernetes.Version = "v1"
	resourceConfig.Kubernetes.Resource = "testresources"
	resourceConfig.Kubernetes.Kind = "TestResource"

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

	collection := store.client.Database(config.Current.Store.Mongo.Database).Collection(resourceConfig.GetGroupVersionName())
	indexCursor, err := collection.Indexes().List(context.Background())
	assertions.NoError(err)

	indexes := make([]bson.M, 0)
	err = indexCursor.All(context.Background(), &indexes)
	assertions.NoError(err)
	assertions.GreaterOrEqual(len(indexes), 1, "at least one index should exist")

	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel), "no errors should be logged")
}

func TestMongoStore_ParseFieldSelectorEdgeCases(t *testing.T) {
	store := &MongoStore{}

	filter, err := store.parseFieldSelector("invalid-format")
	assert.NoError(t, err)
	assert.Empty(t, filter)

	// Test selector with empty right side
	filter, err = store.parseFieldSelector("metadata.name=")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": ""}, filter)

	filter, err = store.parseFieldSelector("metadata.name=value=with=equals")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": "value=with=equals"}, filter)

	filter, err = store.parseFieldSelector("metadata.name=name with spaces")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": "name with spaces"}, filter)

	filter, err = store.parseFieldSelector("metadata.name=special@#$%^&*chars")
	assert.NoError(t, err)
	assert.Equal(t, bson.M{"metadata.name": "special@#$%^&*chars"}, filter)
}

func TestMongoStore_OperationWithBadObject(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	if testing.Short() {
		t.Skip("Skipping operation with bad object test in short mode")
	}

	store := setupMongoStore()
	cleanupMongoCollection()

	// Note: GetMongoId() uses GetUID(), which always has a value (even if empty)
	// Therefore operations will not fail, but execute successfully
	badObject := &unstructured.Unstructured{
		Object: map[string]any{
			// No metadata like name or namespace
			"spec": map[string]any{
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

func TestMongoStore_ErrorHandling(t *testing.T) {
	assertions := assert.New(t)
	defer test.LogRecorder.Reset()

	store := setupMongoStore()

	count, err := store.Count("non_existent_collection")
	assertions.NoError(err)
	assertions.Equal(0, count)

	keys, err := store.Keys("non_existent_collection")
	assertions.NoError(err)
	assertions.Empty(keys)

	result, err := store.Read(testCollectionName, "")
	assertions.ErrorIs(err, ErrResourceNotFound)
	assertions.Nil(result)

	results, err := store.List("non_existent_collection", "", 0)
	assertions.NoError(err)

	// List returns empty slice for empty collection, not nil
	if results != nil {
		assertions.Empty(results)
	}

	results, err = store.List(testCollectionName, "invalid-selector", 0)
	assertions.NoError(err)

	// Should return empty slice if selector is invalid and collection is empty
	if results != nil {
		assertions.Empty(results)
	}
}
