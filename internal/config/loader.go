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
	viper.SetDefault("storeType", "redis")
	viper.SetDefault("namespace", "default")
	viper.SetDefault("reSyncPeriod", "30s")

	viper.SetDefault("redis.host", "localhost")
	viper.SetDefault("redis.port", 6379)
	viper.SetDefault("redis.username", "")
	viper.SetDefault("redis.password", "")
	viper.SetDefault("redis.database", 0)
	viper.SetDefault("redis.initCommands", []string{})

	viper.SetDefault("hazelcast.clusterName", "horizon")
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

	log.Logger = zerolog.New(os.Stdout).Level(logLevel).With().Timestamp().Logger()
	if logLevel == zerolog.DebugLevel {
		log.Logger = log.Logger.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}
}
