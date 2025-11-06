// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"errors"
	"fmt"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"net/http"
)

var server *http.Server

func init() {
	var mux = http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		Timeout: config.Current.Metrics.Timeout,
	}))

	server = &http.Server{
		Addr:    fmt.Sprintf(":%d", config.Current.Metrics.Port),
		Handler: mux,
	}
}

func ExposeMetrics() {
	utils.RegisterShutdownHook(func() {
		_ = server.Shutdown(context.Background())
	}, 3)

	log.Info().Msgf("Metrics will be exposed on port: %d", config.Current.Metrics.Port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error().Err(err).Msg("Could not expose metrics")
	}
}
