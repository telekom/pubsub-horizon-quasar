// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"golang.org/x/exp/maps"
	"strings"
)

var (
	registry *prometheus.Registry
	gauges   map[string]*prometheus.GaugeVec
)

const namespace = "quasar"

func init() {
	registry = prometheus.NewRegistry()
	gauges = make(map[string]*prometheus.GaugeVec)
}

func GetOrCreate(resourceConfig *config.ResourceConfiguration) *prometheus.GaugeVec {
	var gaugeName = resourceConfig.GetCacheName()

	gauge, ok := gauges[gaugeName]
	if !ok {
		gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      fmt.Sprintf("%s_count", strings.ReplaceAll(gaugeName, ".", "_")),
		}, maps.Keys(resourceConfig.Prometheus.Labels))

		gauges[gaugeName] = gauge
		if err := registry.Register(gauge); err != nil {
			var gvr = resourceConfig.GetGroupVersionResource()
			log.Error().Err(err).
				Fields(utils.CreateFieldForResource(&gvr)).
				Msg("Could not create metric")
		}
	}

	return gauge
}

func GetOrCreateCustom(name string) *prometheus.GaugeVec {
	var gaugeName = strings.ReplaceAll(name, ".", "_")

	gauge, ok := gauges[gaugeName]
	if !ok {
		gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      fmt.Sprintf(gaugeName),
		}, []string{})

		gauges[gaugeName] = gauge
		if err := registry.Register(gauge); err != nil {
			log.Error().Err(err).
				Fields(map[string]any{
					"name": fmt.Sprintf("%s_%s", namespace, gaugeName),
				}).
				Msg("Could not create metric")
		}
	}

	return gauge
}
