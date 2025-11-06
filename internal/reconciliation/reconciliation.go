// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Reconciliation struct {
	dataSource DataSource
	resource   *config.Resource
	mu         sync.Mutex
}

type Reconcilable interface {
	Create(obj *unstructured.Unstructured) error
	Count(mapName string) (int, error)
	Keys(mapName string) ([]string, error)
	Connected() bool
}

func NewReconciliation(dataSource DataSource, resource *config.Resource) *Reconciliation {
	return &Reconciliation{
		dataSource: dataSource,
		resource:   resource,
	}
}

func (r *Reconciliation) reconcile(reconcilable Reconcilable) {
	resources, err := r.dataSource.ListResources()

	if err != nil {
		log.Error().Err(err).Fields(map[string]any{
			"cache": r.resource.GetGroupVersionName(),
		}).Msg("Could not retrieve resources from data source")
		return
	}

	mode := config.Current.Store.Hazelcast.ReconcileMode

	switch mode {
	case config.ReconcileModeFull:
		log.Debug().
			Str("cache", r.resource.GetGroupVersionName()).
			Int("count", len(resources)).
			Msg("Performing full reconciliation: inserting all resources")
		for _, item := range resources {
			utils.AddMissingEnvironment(&item)
			if err := reconcilable.Create(&item); err != nil {
				log.Error().Err(err).Fields(utils.CreateFieldsForOp("create", &item)).Msg("Failed to reconcile (full) item")
			}
			log.Debug().
				Fields(utils.CreateFieldsForOp("create", &item)).
				Msg("Reconciled (full) item")
		}

	case config.ReconcileModeIncremental:
		resourceCount := len(resources)
		storeSize, err := reconcilable.Count(r.resource.GetGroupVersionName())
		if err != nil {
			log.Error().Err(err).Fields(map[string]any{
				"cache": r.resource.GetGroupVersionName(),
			}).Msg("Could not get size of store")
			return
		}

		log.Info().Fields(map[string]any{
			"cache":         r.resource.GetGroupVersionName(),
			"storeSize":     storeSize,
			"resourceCount": resourceCount,
		}).Msg("Checking for store size mismatch...")

		if storeSize < resourceCount {
			log.Warn().Fields(map[string]any{
				"cache": r.resource.GetGroupVersionName(),
			}).Msg("Store size does not match resource count. Generating diff for reconciliation...")

			storeKeys, err := reconcilable.Keys(r.resource.GetGroupVersionName())
			if err != nil {
				log.Error().Err(err).Msg("Could no retrieve store keys")
			}

			missingItems := r.generateDiff(resources, storeKeys)
			log.Warn().Msgf("Identified %d missing cache entries. Reprocessing...", len(missingItems))
			for _, item := range missingItems {
				utils.AddMissingEnvironment(&item)
				if err := reconcilable.Create(&item); err != nil {
					log.Error().Err(err).Fields(utils.CreateFieldsForOp("restore", &item)).Msg("Failed to reconcile (diff) item")
				}
				log.Warn().Fields(utils.CreateFieldsForOp("restore", &item)).Msg("Reconciled (diff) item")
			}
		}

	default:
		log.Error().
			Str("cache", r.resource.GetGroupVersionName()).
			Str("mode", mode.String()).
			Msg("Unknown reconciliation mode, skipping")
	}
}

func (r *Reconciliation) generateDiff(resources []unstructured.Unstructured, storeKeys []string) []unstructured.Unstructured {
	var diff = make([]unstructured.Unstructured, 0)
	for _, resource := range resources {
		found := false
		for _, storeKey := range storeKeys {
			if resource.GetName() == storeKey {
				found = true
				break
			}
		}

		if !found {
			diff = append(diff, resource)
		}
	}

	return diff
}

// StartPeriodicReconcile starts a blocking periodic reconciliation process that runs at the specified interval.
func (r *Reconciliation) StartPeriodicReconcile(ctx context.Context, interval time.Duration, reconcilable Reconcilable) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Debug().
		Dur("interval", interval).
		Str("cache", r.resource.GetGroupVersionName()).
		Msg("Starting periodic reconciliation")

	for {
		select {
		case <-ticker.C:
			if !reconcilable.Connected() {
				log.Debug().
					Str("cache", r.resource.GetGroupVersionName()).
					Msg("Skipping timed reconciliation: Hazelcast client disconnected")
				continue
			}
			r.SafeReconcile(reconcilable)
		case <-ctx.Done():
			log.Debug().
				Str("cache", r.resource.GetGroupVersionName()).
				Msg("Stopped periodic reconciliation")
			return
		}
	}
}

func (r *Reconciliation) SafeReconcile(reconcilable Reconcilable) {
	log.Debug().
		Str("cache", r.resource.GetGroupVersionName()).
		Msg("Starting safe reconciliation")

	if !r.mu.TryLock() {
		log.Warn().
			Str("cache", r.resource.GetGroupVersionName()).
			Msg("Reconciliation already in progress, skipping")
		return
	}
	defer r.mu.Unlock()

	defer func() {
		if rec := recover(); rec != nil {
			log.Error().
				Str("cache", r.resource.GetGroupVersionName()).
				Interface("panic", rec).
				Msg("Recovered from panic during reconciliation")
		}
	}()

	r.reconcile(reconcilable)
}
