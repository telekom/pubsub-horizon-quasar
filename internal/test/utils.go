// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"encoding/json"
	"os"

	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/scheme"
)

// CreateTestResource is a test helper function that creates a test resource
func CreateTestResource(name, namespace string, labels map[string]string) *unstructured.Unstructured {
	resource := &unstructured.Unstructured{}
	resource.SetAPIVersion("v1")
	resource.SetKind("TestResource")
	resource.SetName(name)
	if namespace != "" {
		resource.SetNamespace(namespace)
		// Set UID to namespace/name to match expected ID format
		resource.SetUID(types.UID(namespace + "/" + name))
	} else {
		// Set UID to just name if no namespace
		resource.SetUID(types.UID(name))
	}
	if labels != nil {
		resource.SetLabels(labels)
	}
	return resource
}

// CreateTestKubernetesClient creates a mock client for testing
func CreateTestKubernetesClient() *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClient(scheme.Scheme)
}

func ReadTestSubscriptions(file string) []*unstructured.Unstructured {
	bytes, err := os.ReadFile(file)
	if err != nil {
		panic(err)
	}

	var subscriptions []map[string]any
	if err := json.Unmarshal(bytes, &subscriptions); err != nil {
		panic(err)
	}

	var uSubscriptions = make([]*unstructured.Unstructured, 0)
	for _, subscription := range subscriptions {
		var uSubscription = new(unstructured.Unstructured)
		uSubscription.SetUnstructuredContent(subscription)
		uSubscriptions = append(uSubscriptions, uSubscription)
	}

	return uSubscriptions
}

func EnvOrDefault(name string, fallback string) string {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return value
}

func CreateTestResourceConfig() *config.Configuration {
	testConfig := new(config.Configuration)
	testResourceConfig := config.Resource{}
	testResourceConfig.Kubernetes.Group = "subscriber.horizon.telekom.de"
	testResourceConfig.Kubernetes.Version = "v1"
	testResourceConfig.Kubernetes.Resource = "subscriptions"
	testResourceConfig.Kubernetes.Namespace = "playground"
	testResourceConfig.Kubernetes.Kind = "Subscription"
	testConfig.Resources = []config.Resource{testResourceConfig}
	return testConfig
}
