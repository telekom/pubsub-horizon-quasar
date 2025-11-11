// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"golang.org/x/exp/maps"
)

var (
	registry *prometheus.Registry
	gauges   map[string]*prometheus.GaugeVec
	counters = make(map[string]*prometheus.CounterVec)
)

const namespace = "quasar"

func init() {
	registry = prometheus.NewRegistry()
	gauges = make(map[string]*prometheus.GaugeVec)
	counters = make(map[string]*prometheus.CounterVec)
}

func GetOrCreate(resourceConfig *config.Resource) *prometheus.GaugeVec {
	gaugeName := resourceConfig.GetGroupVersionName()

	gauge, ok := gauges[gaugeName]
	if !ok {
		gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      fmt.Sprintf("%s_count", strings.ReplaceAll(gaugeName, ".", "_")),
		}, maps.Keys(resourceConfig.Prometheus.Labels))

		gauges[gaugeName] = gauge
		if err := registry.Register(gauge); err != nil {
			gvr := resourceConfig.GetGroupVersionResource()
			log.Error().Err(err).
				Fields(utils.CreateFieldForResource(&gvr)).
				Msg("Could not create metric")
		}
	}

	return gauge
}

func GetOrCreateCustom(name string) *prometheus.GaugeVec {
	gaugeName := strings.ReplaceAll(name, ".", "_")

	gauge, ok := gauges[gaugeName]
	if !ok {
		gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      gaugeName,
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

func GetOrCreateCustomCounter(name string) *prometheus.CounterVec {
	key := strings.ReplaceAll(name, ".", "_")
	if c, ok := counters[key]; ok {
		return c
	}
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace,
		Name:      key,
		Help:      fmt.Sprintf("Custom counter %s", key),
	}, []string{})
	if err := registry.Register(counter); err != nil {
		log.Error().Err(err).
			Str("metric", namespace+"_"+key).
			Msg("Could not register custom counter")
	}
	counters[key] = counter
	return counter
}
