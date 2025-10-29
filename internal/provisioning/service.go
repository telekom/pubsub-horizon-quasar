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

// ProvisioningService encapsulates the provisioning API service with its dependencies
type ProvisioningService struct {
	app    *fiber.App
	logger *zerolog.Logger
	store  store.DualStore
}

// Setup initializes the Fiber app with routes and middleware
func (s *ProvisioningService) Setup() error {
	if s.store == nil {
		return fmt.Errorf("store is required")
	}
	if s.logger == nil {
		return fmt.Errorf("logger is required")
	}

	s.app = fiber.New(fiber.Config{
		DisableStartupMessage: log.Logger.GetLevel() != zerolog.DebugLevel,
		UnescapePath:          true,
	})

	s.app.Use(fiberzerolog.New(fiberzerolog.Config{
		Logger: s.logger,
	}))

	if config.Current.Provisioning.Security.Enabled {
		s.app.Use(jwtware.New(jwtware.Config{
			JWKSetURLs: config.Current.Provisioning.Security.TrustedIssuers,
		}), withTrustedClients(config.Current.Provisioning.Security.TrustedClients))
	} else {
		s.logger.Warn().Msg("Provisioning service is running without security, this is not recommended for production environments")
	}

	v1 := s.app.Group("/api/v1/resources/:group/:version/:resource")
	v1.Get("/", withGvr, listResources)
	v1.Get("/keys", withGvr, listKeys)
	v1.Get("/count", withGvr, countResources)
	v1.Get("/:id", withGvr, withResourceId, getResource)
	v1.Put("/:id", withGvr, withResourceId, withKubernetesResource, putResource)
	v1.Delete("/:id", withGvr, withResourceId, withKubernetesResource, deleteResource)

	return nil
}

// Start begins listening for HTTP requests
func (s *ProvisioningService) Start(port int) error {
	if s.app == nil {
		return fmt.Errorf("service not setup, call Setup() first")
	}

	s.logger.Info().Int("port", port).Msg("Starting provisioning service...")
	return s.app.Listen(fmt.Sprintf(":%d", port))
}

// Shutdown gracefully shuts down the service
func (s *ProvisioningService) Shutdown(timeout time.Duration) error {
	if s.app == nil {
		return nil
	}

	s.logger.Info().Dur("timeout", timeout).Msg("Shutting down provisioning service...")
	return s.app.ShutdownWithTimeout(timeout)
}

// GetApp returns the underlying Fiber app (useful for testing)
func (s *ProvisioningService) GetApp() *fiber.App {
	return s.app
}

func setupService(logger *zerolog.Logger) {
	service = fiber.New(fiber.Config{
		DisableStartupMessage: log.Logger.GetLevel() != zerolog.DebugLevel,
		UnescapePath:          true,
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

	v1 := service.Group("/api/v1/resources/:group/:version/:resource", validateGroupParam)
	v1.Get("/", withGvr, listResources)
	v1.Get("/keys", withGvr, listKeys)
	v1.Get("/count", withGvr, countResources)
	v1.Get("/:id", withGvr, withResourceId, getResource)
	v1.Put("/:id", withGvr, withResourceId, withKubernetesResource, putResource)
	v1.Delete("/:id", withGvr, withResourceId, withKubernetesResource, deleteResource)
}

func validateGroupParam(c *fiber.Ctx) error {
	group := c.Params("group")
	version := c.Params("version")
	resource := c.Params("resource")

	// check if the provided group/version/resource exists in the configuration
	found := false
	for _, res := range config.Current.Resources {
		if res.Kubernetes.Group == group && res.Kubernetes.Version == version && res.Kubernetes.Resource == resource {
			found = true
			log.Info().Msgf("Successfully validated GVR with: %s/%s/%s", group, version, resource)
			break
		}
	}

	if !found {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": "Invalid group/version/resource combination in path",
		})
	}

	return c.Next()
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

	// Setup store if needed
	if provisioningApiStore == nil {
		setupApiProvisioningStore()
		utils.RegisterShutdownHook(provisioningApiStore.Shutdown, 1)
	}

	for _, resourceConfig := range config.Current.Resources {
		recon := NewReconciliationForProvisioningAPI(provisioningApiStore, &resourceConfig)
		provisioningApiStore.InitializeResource(recon, &resourceConfig)
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

func NewReconciliationForProvisioningAPI(primaryStore store.Store, resource *config.Resource) *reconciliation.Reconciliation {
	dataSource := reconciliation.NewStoreDataSource(primaryStore)
	return reconciliation.NewReconciliation(dataSource, resource)
}
