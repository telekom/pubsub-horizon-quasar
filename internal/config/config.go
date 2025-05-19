// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"github.com/hazelcast/hazelcast-go-client/cluster"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"time"
)

type Configuration struct {
	LogLevel     string                  `mapstructure:"logLevel"`
	ReSyncPeriod time.Duration           `mapstructure:"reSyncPeriod"`
	Resources    []ResourceConfiguration `mapstructure:"resources"`
	Store        struct {
		Type      string                 `mapstructure:"type"`
		Redis     RedisConfiguration     `mapstructure:"redis"`
		Hazelcast HazelcastConfiguration `mapstructure:"hazelcast"`
		Mongo     MongoConfiguration     `mapstructure:"mongo"`
	} `mapstructure:"store"`
	Fallback struct {
		Type  string             `mapstructure:"type"`
		Mongo MongoConfiguration `mapstructure:"mongo"`
	} `mapstructure:"fallback"`
	Metrics MetricsConfiguration `mapstructure:"metrics"`
}

// GetResourceConfiguration returns a resource configuration for the given object if applicable.
// The second return values represents whether the resource exists.
func (c *Configuration) GetResourceConfiguration(obj *unstructured.Unstructured) (*ResourceConfiguration, bool) {
	// As GroupVersionKind and GroupVersionResource define two different things with the first describing a single resource
	// and the latter describing the plural of a custom resource we need to do a name-check and perform a normalization by
	// putting everything into lower-case.
	gvk := obj.GroupVersionKind()

	for _, res := range c.Resources {
		if res.Kubernetes.Group == gvk.Group && res.Kubernetes.Version == gvk.Version && res.Kubernetes.Kind == gvk.Kind {
			return &res, true
		}
	}

	return nil, false
}

type RedisConfiguration struct {
	Host     string `mapstructure:"host"`
	Port     uint   `mapstructure:"port"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
	Database int    `mapstructure:"database"`
}

type HazelcastConfiguration struct {
	ClusterName            string                      `mapstructure:"clusterName"`
	Username               string                      `mapstructure:"username"`
	Password               string                      `mapstructure:"password"`
	Addresses              []string                    `mapstructure:"addresses"`
	WriteBehind            bool                        `mapstructure:"writeBehind"`
	Unisocket              bool                        `mapstructure:"unisocket"`
	ReconcileMode          ReconcileMode               `mapstructure:"reconcileMode"`
	ReconciliationInterval time.Duration               `mapstructure:"reconciliationInterval"`
	HeartbeatTimeout       time.Duration               `mapstructure:"heartbeatTimeout"`
	ConnectionTimeout      time.Duration               `mapstructure:"connectionTimeout"`
	InvocationTimeout      time.Duration               `mapstructure:"invocationTimeout"`
	RedoOperation          bool                        `mapstructure:"redoOperation"`
	ConnectionStrategy     HazelcastConnectionStrategy `mapstructure:"connectionStrategy"`
}

type HazelcastConnectionStrategy struct {
	ReconnectMode cluster.ReconnectMode `mapstructure:"reconnectMode"`
	Timeout       time.Duration         `mapstructure:"timeout"`
	Retry         HazelcastRetry        `mapstructure:"retry"`
}

type HazelcastRetry struct {
	InitialBackoff time.Duration `mapstructure:"initialBackoff"`
	MaxBackoff     time.Duration `mapstructure:"maxBackoff"`
	Multiplier     float64       `mapstructure:"multiplier"`
	Jitter         float64       `mapstructure:"jitter"`
}

type MongoConfiguration struct {
	Uri      string `mapstructure:"uri"`
	Database string `mapstructure:"database"`
}

type MetricsConfiguration struct {
	Enabled bool          `mapstructure:"enabled"`
	Port    int           `mapstructure:"port"`
	Timeout time.Duration `mapstructure:"timeout"`
}
