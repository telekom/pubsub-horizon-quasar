// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/reconciliation"
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

func (s *DummyStore) InitializeResource(reconciliation.DataSource, *config.Resource) {
	s.HasInitializedResource = true
}

func (s *DummyStore) Create(*unstructured.Unstructured) error {
	s.AddCalls++
	return nil
}

func (s *DummyStore) Update(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error {
	_, _ = oldObj, newObj
	s.UpdateCalls++
	return nil
}

func (s *DummyStore) Delete(*unstructured.Unstructured) error {
	s.DeleteCalls++
	return nil
}

func (s *DummyStore) Count(dataset string) (int, error) {
	_ = dataset
	panic("not implemented")
}

func (s *DummyStore) Keys(dataset string) ([]string, error) {
	_ = dataset
	panic("not implemented")
}

func (s *DummyStore) Read(dataset string, key string) (*unstructured.Unstructured, error) {
	_, _ = dataset, key
	panic("not implemented")
}

func (s *DummyStore) List(dataset string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error) {
	_, _, _ = dataset, fieldSelector, limit
	panic("not implemented")
}

func (s *DummyStore) Shutdown() {
	s.IsShutdown = true
}

func (s *DummyStore) Connected() bool { panic("implement me") }
