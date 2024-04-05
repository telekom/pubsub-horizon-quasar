package k8s

import (
	"fmt"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/mongo"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"time"
)

type ResourceWatcher struct {
	resource schema.GroupVersionResource
	informer cache.SharedIndexInformer
	stopChan chan struct{}
}

func NewResourceWatcher(
	client *dynamic.DynamicClient,
	resource schema.GroupVersionResource,
	namespace string,
	reSyncPeriod time.Duration,
) (*ResourceWatcher, error) {

	var informer = createInformer(client, resource, namespace, reSyncPeriod)
	var watcher = ResourceWatcher{
		resource: resource,
		informer: informer,
		stopChan: make(chan struct{}),
	}

	var performReplay = true
	err := informer.SetWatchErrorHandler(func(r *cache.Reflector, err error) {
		if !informer.HasSynced() && config.Current.Fallback.Mongo.Enabled && performReplay {
			performReplay = false
			log.Info().Msg("The informer encountered an error before being in sync. Falling back to MongoDB...")

			var fallbackClient = mongo.NewFallbackClient(config.Current)
			var resource = config.Current.Kubernetes.GetGroupVersionResource()

			replayedDocuments, err := fallbackClient.ReplayForResource(&resource, store.CurrentStore.OnAdd)
			if err != nil {
				log.Fatal().Err(err).Msg("Replay from MongoDB failed!")
			}
			log.Info().Fields(map[string]any{
				"replayedDocuments": replayedDocuments,
			}).Msg("Replay from MongoDB successful!")
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

	return &watcher, err
}

func (w *ResourceWatcher) add(obj any) {
	uObj, ok := obj.(*unstructured.Unstructured)
	if ok {
		utils.AddMissingEnvironment(uObj)
		store.CurrentStore.OnAdd(uObj)
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
		utils.AddMissingEnvironment(uNewObj)
		store.CurrentStore.OnUpdate(uOldObj, uNewObj)
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
		store.CurrentStore.OnDelete(uObj)
		log.Debug().Fields(utils.CreateFieldsForOp("delete", uObj)).Fields("Deleted dataset")
	} else {
		log.Warn().Fields(map[string]any{
			"object":    fmt.Sprintf("%+v", obj),
			"operation": "delete",
		}).Msg("Encountered unexpected object in informer!")
	}
}

func (w *ResourceWatcher) Start() {
	defer func() {
		if err := recover(); err != nil {
			log.Fatal().Fields(map[string]any{
				"error": fmt.Sprintf("%+v", err),
			}).Msg("Informer failed!")
		}
	}()
	w.informer.Run(w.stopChan)
	log.Info().Fields(utils.CreateFieldForResource(&w.resource)).Msg("Resource watcher stopped!")
}

func (w *ResourceWatcher) Stop() {
	close(w.stopChan)
}
