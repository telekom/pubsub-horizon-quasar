package store

import (
	"fmt"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type DummyStore struct{}

func (DummyStore) Initialize() {
	// Nothing to implement here!
}

func (DummyStore) InitializeResource(resourceConfig *config.ResourceConfiguration) {
	// Nothing to implement here!
}

func (DummyStore) OnAdd(obj *unstructured.Unstructured) {
	fmt.Printf("Add: %+v\n", obj.GetName())
}

func (DummyStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) {
	fmt.Printf("Updated: %+v\n", oldObj.GetName())
}

func (DummyStore) OnDelete(obj *unstructured.Unstructured) {
	fmt.Printf("Deleted: %+v\n", obj.GetName())
}

func (DummyStore) Shutdown() {
	// Nothing to implement here!
}
