// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"fmt"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
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

func (s *DummyStore) InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.ResourceConfiguration) {
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

func (s *DummyStore) Count(mapName string) (int, error) {
	panic("not implemented")
}

func (s *DummyStore) Keys(mapName string) ([]string, error) {
	panic("not implemented")
}

func (s *DummyStore) Shutdown() {
	s.IsShutdown = true
}
