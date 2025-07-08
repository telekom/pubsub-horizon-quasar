// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"os"
	"strings"
)

var Current = LoadConfiguration()

func LoadConfiguration() *Configuration {
	setDefaults()
	var config = readConfig()
	applyLogLevel(config.LogLevel)
	return config
}

func setDefaults() {
	viper.AddConfigPath(".")
	viper.SetConfigName("config")
	viper.SetConfigType("yml")

	viper.SetEnvPrefix("quasar")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	viper.SetDefault("logLevel", "info")
	viper.SetDefault("reSyncPeriod", "30s")

	viper.SetDefault("provisioning.enabled", false)
	viper.SetDefault("provisioning.port", 8081)
	viper.SetDefault("provisioning.logLevel", "info")

	viper.SetDefault("provisioning.security.enabled", true)
	viper.SetDefault("provisioning.security.trustedIssuers", []string{"https://auth.example.com/certs"})
	viper.SetDefault("provisioning.security.trustedClient", []string{"example-client"})

	viper.SetDefault("store.type", "hazelcast")

	viper.SetDefault("store.redis.host", "localhost")
	viper.SetDefault("store.redis.port", 6379)
	viper.SetDefault("store.redis.username", "")
	viper.SetDefault("store.redis.password", "")
	viper.SetDefault("store.redis.database", 0)

	viper.SetDefault("store.hazelcast.addresses", []string{})
	viper.SetDefault("store.hazelcast.clusterName", "horizon")
	viper.SetDefault("store.hazelcast.username", "")
	viper.SetDefault("store.hazelcast.password", "")
	viper.SetDefault("store.hazelcast.writeBehind", true)
	viper.SetDefault("store.hazelcast.unisocket", false)
	viper.SetDefault("store.hazelcast.reconcileMode", ReconcileModeFull)
	viper.SetDefault("store.hazelcast.reconciliationInterval", "60s")

	viper.SetDefault("store.hazelcast.heartbeatTimeout", "30s")
	viper.SetDefault("store.hazelcast.connectionTimeout", "30s")
	viper.SetDefault("store.hazelcast.invocationTimeout", "60s")
	viper.SetDefault("store.hazelcast.redoOperatiom", false)
	viper.SetDefault("store.hazelcast.connectionStrategy.timeout", "10m")
	viper.SetDefault("store.hazelcast.connectionStrategy.retry.initialBackoff", "1s")
	viper.SetDefault("store.hazelcast.connectionStrategy.retry.maxBackoff", "10s")
	viper.SetDefault("store.hazelcast.connectionStrategy.retry.multiplier", 1.2)
	viper.SetDefault("store.hazelcast.connectionStrategy.retry.jitter", 0.0)

	viper.SetDefault("store.mongo.uri", "mongodb://localhost:27017")
	viper.SetDefault("store.mongo.database", "horizon")

	viper.SetDefault("resources", []ResourceConfiguration{})

	viper.SetDefault("fallback.type", "mongo")
	viper.SetDefault("fallback.mongo.uri", "mongodb://localhost:27017")
	viper.SetDefault("fallback.mongo.database", "horizon")

	viper.SetDefault("metrics.enabled", false)
	viper.SetDefault("metrics.port", 8080)
	viper.SetDefault("metrics.timeout", "5s")
}

func readConfig() *Configuration {
	if err := viper.ReadInConfig(); err != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if !errors.As(err, &configFileNotFoundError) {
			log.Fatal().Err(err).Msg("Could not read configuration!")
		}
	}

	viper.AutomaticEnv()

	var config Configuration
	if err := viper.Unmarshal(&config); err != nil {
		log.Fatal().Err(err).Msg("Could not unmarshal configuration!")
	}

	return &config
}

func applyLogLevel(level string) {
	logLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		logLevel = zerolog.InfoLevel
		log.Info().Msgf("Invalid log level %s. Info log level is used", logLevel)
	}

	log.Logger = log.Logger.Level(logLevel).With().Timestamp().Logger()
	if logLevel == zerolog.DebugLevel {
		log.Logger = log.Logger.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}
}
