//go:build testing

package test

import (
	"fmt"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type DummyStore struct {
	AddCalls               int
	UpdateCalls            int
	DeleteCalls            int
	IsInitialized          bool
	HasInitializedResource bool
	IsShutdown             bool
}

func (s *DummyStore) Initialize() {
	s.IsInitialized = true
}

func (s *DummyStore) InitializeResource(resourceConfig *config.ResourceConfiguration) {
	s.HasInitializedResource = true
}

func (s *DummyStore) OnAdd(obj *unstructured.Unstructured) {
	fmt.Printf("Add: %+v\n", obj.GetName())
	s.AddCalls++
}

func (s *DummyStore) OnUpdate(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) {
	fmt.Printf("Updated: %+v\n", oldObj.GetName())
	s.UpdateCalls++
}

func (s *DummyStore) OnDelete(obj *unstructured.Unstructured) {
	fmt.Printf("Deleted: %+v\n", obj.GetName())
	s.DeleteCalls++
}

func (s *DummyStore) Shutdown() {
	s.IsShutdown = true
}
