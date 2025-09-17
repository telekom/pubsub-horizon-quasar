//go:build testing

package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.parseFieldSelector(tt.fieldSelector)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedFilter, result)
		})
	}
}

func TestMongoStore_ParseLabelSelector(t *testing.T) {
	store := &MongoStore{}

	tests := []struct {
		name           string
		labelSelector  string
		expectedFilter bson.M
	}{
		{
			name:           "empty selector",
			labelSelector:  "",
			expectedFilter: bson.M{},
		},
		{
			name:          "single label selector",
			labelSelector: "app=test",
			expectedFilter: bson.M{
				"metadata.labels.app": "test",
			},
		},
		{
			name:          "multiple label selectors",
			labelSelector: "app=test,version=v1",
			expectedFilter: bson.M{
				"metadata.labels.app":     "test",
				"metadata.labels.version": "v1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.parseLabelSelector(tt.labelSelector)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedFilter, result)
		})
	}
}

// Helper function to create test unstructured resource
func createTestResource(name, namespace string, labels map[string]string) *unstructured.Unstructured {
	resource := &unstructured.Unstructured{}
	resource.SetAPIVersion("v1")
	resource.SetKind("TestResource")
	resource.SetName(name)
	if namespace != "" {
		resource.SetNamespace(namespace)
	}
	if labels != nil {
		resource.SetLabels(labels)
	}
	return resource
}