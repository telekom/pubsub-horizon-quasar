// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"time"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/metrics"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/utils"
)

func scheduleMetricGeneration(store store.Store, resourceConfig *config.Resource) {
	go func() {
		for {
			resources, err := store.List(resourceConfig.GetGroupVersionName(), "", 0)
			if err != nil {
				log.Error().Str("task", "metrics").Err(err).Msg("Error listing resources for metric generation")
				time.Sleep(config.Current.Metrics.Timeout)
				continue
			}

			gauge := metrics.GetOrCreate(resourceConfig)
			gauge.Reset()

			for _, resource := range resources {
				gauge.With(utils.GetLabelsForResource(&resource, resourceConfig)).Set(1)
			}

			time.Sleep(config.Current.Metrics.Timeout)
		}
	}()
}
