// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
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
	"sync"
	"time"
)

var (
	service              *fiber.App
	logger               *zerolog.Logger
	provisioningApiStore store.DualStore
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
	v1.Get("/", withGvr, listResources)
	v1.Get("/keys", withGvr, listKeys)
	v1.Get("/count", withGvr, countResources)
	v1.Get("/:id", withGvr, withResourceId, getResource)
	v1.Put("/:id", withGvr, withResourceId, withKubernetesResource, putResource)
	v1.Delete("/:id", withGvr, withResourceId, withKubernetesResource, deleteResource)
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
	var primaryStoreType = provisioningConfig.Primary.Type
	var secondaryStoreType = provisioningConfig.Secondary.Type

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

	if provisioningApiStore == nil {
		setupApiProvisioningStore()
		utils.RegisterShutdownHook(provisioningApiStore.Shutdown, 1)

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		var wg sync.WaitGroup

		logger.Info().Msg("Starting asynchronous MongoDB to Hazelcast synchronization")
		RunAsyncMongoToHazelcastSync(ctx, provisioningApiStore, &wg, *logger)

		utils.RegisterShutdownHook(func() {
			logger.Info().Msg("Waiting for background synchronization to complete before shutdown")
			cancel()
			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
				logger.Info().Msg("Background synchronization completed")
			case <-time.After(30 * time.Second):
				logger.Warn().Msg("Background synchronization did not complete within timeout")
			}
		}, 2) // priority 2, to be executed after store shutdown but before service shutdown
	}

	if service == nil {
		setupService(logger)
	}

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

	logger.Info().Int("port", port).Msg("Starting provisioning service...")
	if err := service.Listen(fmt.Sprintf(":%d", port)); err != nil {
		log.Fatal().Err(err).Msg("Failed to start provisioning service")
	}
}
