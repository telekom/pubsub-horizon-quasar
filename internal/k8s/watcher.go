// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/fallback"
	"github.com/telekom/quasar/internal/metrics"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/utils"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
)

var WatcherStore store.Store

type ResourceWatcher struct {
	client         dynamic.Interface
	resourceConfig *config.ResourceConfiguration
	informer       cache.SharedIndexInformer
	stopChan       chan struct{}
}

func NewResourceWatcher(
	client dynamic.Interface,
	resourceConfig *config.ResourceConfiguration,
	reSyncPeriod time.Duration,
) (*ResourceWatcher, error) {

	var resource = resourceConfig.GetGroupVersionResource()
	var namespace = resourceConfig.Kubernetes.Namespace
	var informer = createInformer(client, resource, namespace, reSyncPeriod)
	var watcher = ResourceWatcher{
		client:         client,
		resourceConfig: resourceConfig,
		informer:       informer,
		stopChan:       make(chan struct{}),
	}

	var performReplay = true
	err := informer.SetWatchErrorHandler(func(r *cache.Reflector, err error) {
		if !informer.HasSynced() && performReplay {
			performReplay = false
			log.Info().Msg("The informer encountered an error before being in sync. Falling back to MongoDB...")

			var resource = resourceConfig.GetGroupVersionResource()

			replayedDocuments, err := fallback.CurrentFallback.ReplayResource(&resource, WatcherStore.Create)
			if err != nil {
				log.Fatal().Err(err).Msg("Replay from MongoDB failed!")
			}
			log.Info().Fields(map[string]any{
				"replayedDocuments": replayedDocuments,
			}).Msg("Replay from MongoDB successful!")
		} else {
			log.Fatal().Err(err).Msg("Watcher failed. Terminating...")
		}
	})
	if err != nil {
		return nil, err
	}

	_, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    watcher.add,
		UpdateFunc: watcher.update,
		DeleteFunc: watcher.delete,
	})

	go watcher.collectMetrics(client, resourceConfig)

	return &watcher, err
}

func (w *ResourceWatcher) add(obj any) {
	uObj, ok := obj.(*unstructured.Unstructured)
	if ok {
		utils.AddMissingEnvironment(uObj)
		WatcherStore.Create(uObj)

		if config.Current.Metrics.Enabled && w.resourceConfig.Prometheus.Enabled {
			var labels = utils.GetLabelsForResource(uObj, w.resourceConfig)
			metrics.GetOrCreate(w.resourceConfig).With(labels).Inc()
		}

		log.Debug().Fields(utils.CreateFieldsForOp("add", uObj)).Msg("Added dataset")
	} else {
		log.Warn().Fields(map[string]any{
			"object":    fmt.Sprintf("%+v", obj),
			"operation": "add",
		}).Msg("Encountered unexpected object in informer!")
	}
}

func (w *ResourceWatcher) update(oldObj any, newObj any) {
	uOldObj, oldOk := oldObj.(*unstructured.Unstructured)
	uNewObj, newOk := newObj.(*unstructured.Unstructured)
	if oldOk && newOk {
		if uNewObj.GetResourceVersion() == uOldObj.GetResourceVersion() {
			return
		}

		utils.AddMissingEnvironment(uNewObj)
		WatcherStore.Update(uOldObj, uNewObj)
		log.Debug().Fields(utils.CreateFieldsForOp("update", uOldObj)).Msg("Updated dataset")
	} else {
		log.Warn().Fields(map[string]any{
			"oldObject": fmt.Sprintf("%+v", uOldObj),
			"newObject": fmt.Sprintf("%+v", uNewObj),
			"operation": "update",
		}).Msg("Encountered unexpected object in informer!")
	}
}

func (w *ResourceWatcher) delete(obj any) {
	uObj, ok := obj.(*unstructured.Unstructured)
	if ok {
		WatcherStore.Delete(uObj)
		log.Debug().Fields(utils.CreateFieldsForOp("delete", uObj)).Fields("Deleted dataset")

		if config.Current.Metrics.Enabled && w.resourceConfig.Prometheus.Enabled {
			var labels = utils.GetLabelsForResource(uObj, w.resourceConfig)
			metrics.GetOrCreate(w.resourceConfig).With(labels).Dec()
		}
	} else {
		log.Warn().Fields(map[string]any{
			"object":    fmt.Sprintf("%+v", obj),
			"operation": "delete",
		}).Msg("Encountered unexpected object in informer!")
	}
}

func (w *ResourceWatcher) Start() {
	WatcherStore.InitializeResource(w.client, w.resourceConfig)

	defer func() {
		if err := recover(); err != nil {
			log.Panic().Fields(map[string]any{
				"error": fmt.Sprintf("%+v", err),
			}).Msg("Informer failed!")
		}
	}()
	w.informer.Run(w.stopChan)

	var resource = w.resourceConfig.GetGroupVersionResource()
	log.Info().Fields(utils.CreateFieldForResource(&resource)).Msg("Resource watcher stopped!")
}

func (w *ResourceWatcher) Stop() {
	close(w.stopChan)
}

func (w *ResourceWatcher) collectMetrics(client dynamic.Interface, resourceConfig *config.ResourceConfiguration) {
	if err := recover(); err != nil {
		log.Error().Msgf("Recovered from %s during kubernetes metric collection", err)
		return
	}

	for {
		list, err := client.Resource(resourceConfig.GetGroupVersionResource()).
			Namespace(resourceConfig.Kubernetes.Namespace).
			List(context.Background(), v1.ListOptions{})

		if err != nil {
			log.Error().Err(err).Fields(map[string]any{
				"resource": resourceConfig.GetCacheName(),
			}).Msg("Could not resource count")

			time.Sleep(15 * time.Second)
			continue
		}

		var gaugeName = resourceConfig.GetCacheName() + "_kubernetes_count"
		metrics.GetOrCreateCustom(gaugeName).WithLabelValues().Set(float64(len(list.Items)))
		time.Sleep(15 * time.Second)
	}
}

func SetupWatcherStore() {
	var primaryType = config.Current.Watcher.Store.Primary.Type
	var secondaryType = config.Current.Watcher.Store.Secondary.Type

	var err error
	WatcherStore, err = store.SetupDualStoreManager("WatcherStore", primaryType, secondaryType)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"primaryType":   primaryType,
			"secondaryType": secondaryType,
		}).Err(err).Msg("Could not create k8s watcher store manager!")
	}
}
