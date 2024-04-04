package config

import "time"

type Configuration struct {
	LogLevel     string        `mapstructure:"logLevel"`
	Namespace    string        `mapstructure:"namespace"`
	ReSyncPeriod time.Duration `mapstructure:"reSyncPeriod"`
	Store        struct {
		StoreType string                 `mapstructure:"storeType"`
		Redis     RedisConfiguration     `mapstructure:"redis"`
		Hazelcast HazelcastConfiguration `mapstructure:"hazelcast"`
	} `mapstructure:"store"`
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
	Mongo       MongoConfiguration `mapstructure:"mongo"`
}

type MongoConfiguration struct {
	Enabled  bool   `mapstructure:"enabled"`
	Url      string `mapstructure:"url"`
	Database string `mapstructure:"database"`
}
