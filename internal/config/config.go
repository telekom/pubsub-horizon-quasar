// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
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

func (c *Configuration) GetResourceConfiguration(obj *unstructured.Unstructured) (*ResourceConfiguration, bool) {
	gvk := obj.GroupVersionKind()
	gvr := schema.GroupVersionResource{
		Group:   gvk.Group,
		Version: gvk.Version,
	}

	resource := gvk.Kind
	if strings.HasSuffix(resource, "s") {
		resource = strings.ToLower(resource)
	} else {
		resource = strings.ToLower(resource) + "s"
	}
	gvr.Resource = resource

	for _, res := range c.Resources {
		if res.Kubernetes.Group == gvr.Group && res.Kubernetes.Version == gvr.Version && res.Kubernetes.Resource == gvr.Resource {
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
	ClusterName string   `mapstructure:"clusterName"`
	Username    string   `mapstructure:"username"`
	Password    string   `mapstructure:"password"`
	Addresses   []string `mapstructure:"addresses"`
	WriteBehind bool     `mapstructure:"writeBehind"`
	Unisocket   bool     `mapstructure:"unisocket"`
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
