//go:build testing

package test

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"os"
)

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
