// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"os"
	"time"

	"github.com/gofiber/contrib/fiberzerolog"
	jwtware "github.com/gofiber/contrib/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/healthcheck"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/reconciliation"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/utils"
)

var (
	service              *fiber.App
	logger               *zerolog.Logger
	provisioningApiStore store.DualStore
)

func setupService(logger *zerolog.Logger) {
	service = fiber.New(fiber.Config{
		DisableStartupMessage: log.Logger.GetLevel() != zerolog.DebugLevel,
		ErrorHandler:          handleErrors,
	})

	service.Use(fiberzerolog.New(fiberzerolog.Config{
		Logger: logger,
	}))

	service.Use(healthcheck.New())

	if config.Current.Provisioning.Security.Enabled {
		service.Use(jwtware.New(jwtware.Config{
			JWKSetURLs: config.Current.Provisioning.Security.TrustedIssuers,
		}), withTrustedClients(config.Current.Provisioning.Security.TrustedClients))
	} else {
		log.Warn().Msg("Provisioning service is running without security, this is not recommended for production environments")
	}

	v1 := service.Group("/api/v1/resources/:group/:version/:resource", withGvr)
	v1.Get("/", listResources)
	v1.Get("/keys", listKeys)
	v1.Get("/count", countResources)
	v1.Get("/:id", withResourceId, getResource)
	v1.Put("/:id", withResourceId, withKubernetesResource, putResource)
	v1.Delete("/:id", withResourceId, withKubernetesResource, deleteResource)
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
	provisioningConfig := config.Current.Provisioning.Store
	primaryStoreType := provisioningConfig.Primary.Type
	secondaryStoreType := provisioningConfig.Secondary.Type

	var err error
	provisioningApiStore, err = store.SetupDualStoreManager("ProvisioningAPIStore", primaryStoreType, secondaryStoreType)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"primaryStoreType":   primaryStoreType,
			"secondaryStoreType": secondaryStoreType,
		}).Err(err).Msg("Could not create provisioning store manager!")
	}
}

func Listen(port int) {
	if logger == nil {
		logger = createLogger()
	}

	// Setup store if needed and initialize its resources
	if provisioningApiStore == nil {
		setupApiProvisioningStore()
		utils.RegisterShutdownHook(provisioningApiStore.Shutdown, 1)
	}

	for _, resourceConfig := range config.Current.Resources {
		reconciliationSource := reconciliation.NewDataSourceFromStore(provisioningApiStore, resourceConfig)
		provisioningApiStore.InitializeResource(reconciliationSource, &resourceConfig)
		scheduleMetricGeneration(provisioningApiStore, &resourceConfig)
	}

	setupService(logger)

	utils.RegisterShutdownHook(func() {
		timeout := 30 * time.Second
		logger.Info().Dur("timeout", timeout).Msg("Shutting down provisioning service...")
		if provisioningApiStore != nil {
			provisioningApiStore.Shutdown()
		}
		if err := service.ShutdownWithTimeout(timeout); err != nil {
			logger.Error().Err(err).Msg("Failed to shutdown provisioning service gracefully")
		}
	}, 1)

	// Start provisioning http service
	logger.Info().Int("port", port).Msg("Starting provisioning http service...")
	if err := service.Listen(fmt.Sprintf(":%d", port)); err != nil {
		log.Error().Err(err).Msg("Failed to start provisioning http service")
		utils.GracefulShutdown()
	}
}
