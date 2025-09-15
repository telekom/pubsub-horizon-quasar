// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"github.com/gofiber/contrib/fiberzerolog"
	jwtware "github.com/gofiber/contrib/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/utils"
	"os"
	"time"
)

var (
	service      *fiber.App
	logger       *zerolog.Logger
	storeManager store.DualStore
)

func setupService(logger *zerolog.Logger) {
	service = fiber.New(fiber.Config{
		DisableStartupMessage: log.Logger.GetLevel() != zerolog.DebugLevel,
		UnescapePath:          true,
	})

	service.Use(fiberzerolog.New(fiberzerolog.Config{
		Logger: logger,
	}))

	if config.Current.Provisioning.Security.Enabled {
		service.Use(jwtware.New(jwtware.Config{
			JWKSetURLs: config.Current.Provisioning.Security.TrustedIssuers,
		}), withTrustedClients(config.Current.Provisioning.Security.TrustedClients))
	} else {
		log.Warn().Msg("Provisioning service is running without security, this is not recommended for production environments")
	}

	v1 := service.Group("/api/v1/resources/:group/:version/:resource")
	v1.Post("/", withGvr, withKubernetesResource, putProvision)
	v1.Put("/", withGvr, withKubernetesResource, putProvision)
	v1.Delete("/", withGvr, withKubernetesResource, deleteProvision)
}

func createLogger() *zerolog.Logger {
	logger := log.Logger.With().Str("logger", "provisioning").Logger()

	lvl, err := zerolog.ParseLevel(config.Current.Provisioning.LogLevel)
	if err != nil {
		logger.Error().Err(err).Msg("Invalid log level for provisioning service, defaulting to info")
		lvl = zerolog.InfoLevel
	}

	logger = logger.Level(lvl)

	if lvl == zerolog.DebugLevel {
		logger = logger.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}

	return &logger
}

func setupApiProvisioningStore() {
	var provisioningConfig = config.Current.Provisioning.Store
	var primaryType = provisioningConfig.Primary
	var secondaryType = provisioningConfig.Secondary

	if primaryType == "" {
		primaryType = "mongo"
	}
	if secondaryType == "" {
		secondaryType = "hazelcast"
	}

	var err error
	storeManager, err = store.SetupStoreManager(primaryType, secondaryType)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"primaryType":   primaryType,
			"secondaryType": secondaryType,
		}).Err(err).Msg("Could not create provisioning store manager!")
	}
}

func Listen(port int) {
	if logger == nil {
		logger = createLogger()
	}

	if service == nil {
		setupService(logger)
	}

	if storeManager == nil {
		setupApiProvisioningStore()
		utils.RegisterShutdownHook(storeManager.Shutdown, 1)
	}

	utils.RegisterShutdownHook(func() {
		timeout := 30 * time.Second
		logger.Info().Dur("timeout", timeout).Msg("Shutting down provisioning service...")
		if storeManager != nil {
			storeManager.Shutdown()
		}
		if err := service.ShutdownWithTimeout(timeout); err != nil {
			logger.Error().Err(err).Msg("Failed to shutdown provisioning service gracefully")
		}
	}, 1)

	logger.Info().Int("port", port).Msg("Starting provisioning service...")
	if err := service.Listen(fmt.Sprintf(":%d", port)); err != nil {
		log.Fatal().Err(err).Msg("Failed to start provisioning service")
	}
}
