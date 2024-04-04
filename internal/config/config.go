package config

import "time"

type Configuration struct {
	LogLevel     string                 `mapstructure:"logLevel"`
	StoreType    string                 `mapstructure:"storeType"`
	Namespace    string                 `mapstructure:"namespace"`
	ReSyncPeriod time.Duration          `mapstructure:"reSyncPeriod"`
	Redis        RedisConfiguration     `mapstructure:"redis"`
	Hazelcast    HazelcastConfiguration `mapstructure:"hazelcast"`
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
	ClusterName string `mapstructure:"clusterName"`
}
