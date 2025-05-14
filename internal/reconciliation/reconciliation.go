// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"context"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sync"
	"time"
)

type Reconciliation struct {
	client   dynamic.Interface
	resource *config.ResourceConfiguration
	mu       sync.Mutex
}

type Reconcilable interface {
	OnAdd(obj *unstructured.Unstructured)
	Count(mapName string) (int, error)
	Keys(mapName string) ([]string, error)
	Connected() bool
}

func NewReconciliation(client dynamic.Interface, resource *config.ResourceConfiguration) *Reconciliation {
	return &Reconciliation{client: client, resource: resource}
}

func (r *Reconciliation) reconcile(reconcilable Reconcilable) {
	resources, err := r.client.Resource(r.resource.GetGroupVersionResource()).
		Namespace(r.resource.Kubernetes.Namespace).
		List(context.Background(), v1.ListOptions{})

	if err != nil {
		log.Error().Err(err).Fields(map[string]any{
			"cache": r.resource.GetCacheName(),
		}).Msg("Could not retrieve resources from cluster")
		return
	}

	mode := config.Current.Store.Hazelcast.ReconcileMode

	switch mode {
	case config.Full:
		log.Info().
			Str("cache", r.resource.GetCacheName()).
			Int("count", len(resources.Items)).
			Msg("Performing full reconciliation: inserting all resources")
		for _, item := range resources.Items {
			reconcilable.OnAdd(&item)
			log.Info().
				Fields(utils.CreateFieldsForOp("add", &item)).
				Msg("Reconciled (full)")
		}

	case config.Incremental:
		resourceCount := len(resources.Items)
		storeSize, err := reconcilable.Count(r.resource.GetCacheName())
		if err != nil {
			log.Error().Err(err).Fields(map[string]any{
				"cache": r.resource.GetCacheName(),
			}).Msg("Could not get size of store")
			return
		}

		log.Info().Fields(map[string]any{
			"cache":         r.resource.GetCacheName(),
			"storeSize":     storeSize,
			"resourceCount": resourceCount,
		}).Msg("Checking for store size mismatch...")

		if storeSize < resourceCount {
			log.Warn().Fields(map[string]any{
				"cache": r.resource.GetCacheName(),
			}).Msg("Store size does not match resource count. Generating diff for reconciliation...")

			storeKeys, err := reconcilable.Keys(r.resource.GetCacheName())
			if err != nil {
				log.Error().Err(err).Msg("Could no retrieve store keys")
			}

			missingEntries := r.generateDiff(resources.Items, storeKeys)
			log.Warn().Msgf("Identified %d missing cache entries. Reprocessing...", len(missingEntries))
			for _, entry := range missingEntries {
				reconcilable.OnAdd(&entry)
				log.Warn().Fields(utils.CreateFieldsForOp("restore", &entry)).Msg("Restored")
			}
		}

	default:
		log.Error().
			Str("cache", r.resource.GetCacheName()).
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

func (r *Reconciliation) StartPeriodicReconcile(ctx context.Context, interval time.Duration, reconcilable Reconcilable) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	log.Info().
		Dur("interval", interval).
		Str("cache", r.resource.GetCacheName()).
		Msg("Starting periodic reconciliation")

	for {
		select {
		case <-ticker.C:
			if !reconcilable.Connected() {
				log.Info().
					Str("cache", r.resource.GetCacheName()).
					Msg("Skipping reconciliation: Hazelcast client disconnected")
				continue
			}
			r.SafeReconcile(reconcilable)
		case <-ctx.Done():
			log.Info().
				Str("cache", r.resource.GetCacheName()).
				Msg("Stopped periodic reconciliation")
			return
		}
	}
}

func (r *Reconciliation) SafeReconcile(reconcilable Reconcilable) {
	log.Info().
		Str("cache", r.resource.GetCacheName()).
		Msg("Starting safe reconciliation")

	if !r.mu.TryLock() {
		log.Warn().
			Str("cache", r.resource.GetCacheName()).
			Msg("Reconciliation already in progress, skipping")
		return
	}
	defer r.mu.Unlock()

	defer func() {
		if rec := recover(); rec != nil {
			log.Error().
				Str("cache", r.resource.GetCacheName()).
				Interface("panic", rec).
				Msg("Recovered from panic during reconciliation")
		}
	}()

	r.reconcile(reconcilable)
}
