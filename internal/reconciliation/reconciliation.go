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
)

type Reconciliation struct {
	client   dynamic.Interface
	resource *config.ResourceConfiguration
}

type Reconcilable interface {
	OnAdd(obj *unstructured.Unstructured)
	Count(mapName string) (int, error)
	Keys(mapName string) ([]string, error)
}

func NewReconciliation(client dynamic.Interface, resource *config.ResourceConfiguration) *Reconciliation {
	return &Reconciliation{client, resource}
}

func (r *Reconciliation) Reconcile(reconcilable Reconcilable) {
	resources, err := r.client.Resource(r.resource.GetGroupVersionResource()).
		Namespace(r.resource.Kubernetes.Namespace).
		List(context.Background(), v1.ListOptions{})

	if err != nil {
		log.Error().Err(err).Fields(map[string]any{
			"cache": r.resource.GetCacheName(),
		}).Msg("Could not retrieve resources from cluster")
		return
	}

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
			utils.AddMissingEnvironment(&entry)
			reconcilable.OnAdd(&entry)
			log.Warn().Fields(utils.CreateFieldsForOp("restore", &entry)).Msg("Restored")
		}
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
