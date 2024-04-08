package config

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"time"
)

type Configuration struct {
	LogLevel     string                  `mapstructure:"logLevel"`
	Namespace    string                  `mapstructure:"namespace"`
	ReSyncPeriod time.Duration           `mapstructure:"reSyncPeriod"`
	Kubernetes   KubernetesConfiguration `mapstructure:"kubernetes"`
	Store        struct {
		StoreType string                 `mapstructure:"storeType"`
		Redis     RedisConfiguration     `mapstructure:"redis"`
		Hazelcast HazelcastConfiguration `mapstructure:"hazelcast"`
	} `mapstructure:"store"`
	Fallback struct {
		Mongo MongoConfiguration `mapstructure:"mongo"`
	} `mapstructure:"fallback"`
}

type RedisConfiguration struct {
	Host         string   `mapstructure:"host"`
	Port         uint     `mapstructure:"port"`
	Username     string   `mapstructure:"username"`
	Password     string   `mapstructure:"password"`
	Database     int      `mapstructure:"database"`
	InitCommands []string `mapstructure:"initCommands"`
}

type HazelcastConfiguration struct {
	ClusterName string             `mapstructure:"clusterName"`
	Username    string             `mapstructure:"username"`
	Password    string             `mapstructure:"password"`
	Mongo       MongoConfiguration `mapstructure:"mongo"`
	Indexes     []HazelcastIndex   `mapstructure:"indexes"`
}

type HazelcastIndex struct {
	Name   string   `mapstructure:"name"`
	Fields []string `mapstructure:"fields"`
	Type   string   `mapstructure:"type"`
}

type MongoConfiguration struct {
	Enabled  bool         `mapstructure:"enabled"`
	Uri      string       `mapstructure:"uri"`
	Database string       `mapstructure:"database"`
	Indexes  []MongoIndex `mapstructure:"indexes"`
}

type MongoIndex struct {
	Fields map[string]int `mapstructure:"fields"`
}

func (i *MongoIndex) ToIndexModel() mongo.IndexModel {
	var keys = bson.D{}
	for field, direction := range i.Fields {
		keys = append(keys, bson.E{Key: field, Value: direction})
	}

	return mongo.IndexModel{
		Keys: keys,
	}
}

type KubernetesConfiguration struct {
	Group    string `mapstrucutre:"group"`
	Version  string `mapstructure:"version"`
	Resource string `mapstructure:"resource"`
}

func (c *KubernetesConfiguration) GetGroupVersionResource() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    c.Group,
		Version:  c.Version,
		Resource: c.Resource,
	}
}
