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
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/client-go/dynamic"
	"os"
	"time"
)

var (
	service          *fiber.App
	logger           *zerolog.Logger
	KubernetesClient *dynamic.DynamicClient
)

func setupService(logger *zerolog.Logger) {
	service = fiber.New(fiber.Config{
		DisableStartupMessage: log.Logger.GetLevel() != zerolog.DebugLevel,
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

	v1 := service.Group("/api/v1/:group/:version/:resource")
	v1.Post("/", withGvr, withKubernetesResource, putProvision)
	v1.Put("/", withGvr, withKubernetesResource, putProvision)
	v1.Delete("/:namespace/:name", deleteProvision)
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

func Listen(port int) {
	if logger == nil {
		logger = createLogger()
	}

	if service == nil {
		setupService(logger)
	}

	utils.RegisterShutdownHook(func() {
		timeout := 30 * time.Second
		logger.Info().Dur("timeout", timeout).Msg("Shutting down provisioning service...")
		if err := service.ShutdownWithTimeout(timeout); err != nil {
			logger.Error().Err(err).Msg("Failed to shutdown provisioning service gracefully")
		}
	}, 1)

	logger.Info().Int("port", port).Msg("Starting provisioning service...")
	if err := service.Listen(fmt.Sprintf(":%d", port)); err != nil {
		log.Fatal().Err(err).Msg("Failed to start provisioning service")
	}
}
