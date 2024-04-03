package k8s

import "k8s.io/apimachinery/pkg/runtime/schema"

var ResourceSubscription = schema.GroupVersionResource{
	Group:    "subscriber.horizon.telekom.de",
	Resource: "subscriptions",
	Version:  "v1",
}
