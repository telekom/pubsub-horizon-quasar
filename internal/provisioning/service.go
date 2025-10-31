// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"fmt"
	"time"

	"os"

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

	// Setup store if needed and initialize its resources
	if provisioningApiStore == nil {
		setupApiProvisioningStore()
		utils.RegisterShutdownHook(provisioningApiStore.Shutdown, 1)
	}

	for _, resourceConfig := range config.Current.Resources {
		reconciliationSource := reconciliation.NewDataSourceFromStore(provisioningApiStore, resourceConfig)
		provisioningApiStore.InitializeResource(reconciliationSource, &resourceConfig)
	}

	// Setup HTTP service
	if service == nil {
		setupService(logger)
	}

	// Register shutdown hook
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

	// Start HTTP server in goroutine so it can accept health/readiness probes
	// while cache population is running
	serverStarted := make(chan struct{})
	serverError := make(chan error, 1)

	go func() {
		logger.Info().Int("port", port).Msg("Starting HTTP server (not yet ready for API requests)...")
		close(serverStarted) // Signal that we're about to start listening
		if err := service.Listen(fmt.Sprintf(":%d", port)); err != nil {
			serverError <- err
		}
	}()

	// Wait for server to start
	<-serverStarted
	time.Sleep(100 * time.Millisecond) // Brief pause to ensure server is bound to port

	// Check if server failed to start
	select {
	case err := <-serverError:
		log.Fatal().Err(err).Msg("Failed to start HTTP server")
	default:
		logger.Info().Msg("HTTP server started, /health and /ready endpoints available")
	}

	// Block here waiting for server to exit (on shutdown signal or error)
	select {
	case err := <-serverError:
		log.Fatal().Err(err).Msg("HTTP server stopped unexpectedly")
	}
}
