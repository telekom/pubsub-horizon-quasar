// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"github.com/hazelcast/hazelcast-go-client/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
)

type ResourceConfiguration struct {
	Kubernetes struct {
		Group     string `mapstructure:"group"`
		Version   string `mapstructure:"version"`
		Resource  string `mapstructure:"resource"`
		Namespace string `mapstructure:"namespace"`
	} `mapstructure:"kubernetes"`
	MongoIndexes     []MongoResourceIndex     `mapstructure:"mongoIndexes"`
	HazelcastIndexes []HazelcastResourceIndex `mapstructure:"hazelcastIndexes"`
	Prometheus       PrometheusConfiguration  `mapstructure:"prometheus"`
}

func (c *ResourceConfiguration) GetGroupVersionResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    c.Kubernetes.Group,
		Version:  c.Kubernetes.Version,
		Resource: c.Kubernetes.Resource,
	}
}

func (c *ResourceConfiguration) GetCacheName() string {
	var gvr = c.GetGroupVersionResource()
	var name = fmt.Sprintf("%s.%s.%s", gvr.Resource, gvr.Group, gvr.Version)
	return strings.ToLower(name)
}

type MongoResourceIndex map[string]int

func (i MongoResourceIndex) ToIndexModel() mongo.IndexModel {
	var keys = make(bson.D, 0)
	for key, value := range i {
		keys = append(keys, bson.E{Key: key, Value: value})
	}

	return mongo.IndexModel{
		Keys: keys,
	}
}

type HazelcastResourceIndex struct {
	Name   string   `mapstructure:"name"`
	Fields []string `mapstructure:"fields"`
	Type   string   `mapstructure:"type"`
}

func (i *HazelcastResourceIndex) translateIndexType() types.IndexType {
	switch strings.ToLower(i.Type) {
	case "hash":
		return types.IndexTypeHash
	case "sorted":
		return types.IndexTypeSorted
	default:
		panic("Unsupported index type " + i.Type)
	}
}

func (i *HazelcastResourceIndex) ToIndexConfig() types.IndexConfig {
	return types.IndexConfig{
		Name:       i.Name,
		Attributes: i.Fields,
		Type:       i.translateIndexType(),
	}
}
