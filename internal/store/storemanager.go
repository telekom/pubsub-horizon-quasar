// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"sync"
)

type DualStore interface {
	Store
	GetPrimary() Store
	GetSecondary() Store
}

// DualStoreManager handles primary and secondary store
type DualStoreManager struct {
	primary      Store
	secondary    Store
	mu           sync.RWMutex
	errorHandler ErrorHandler
}

// ErrorHandler handles dual store operation errors
type ErrorHandler interface {
	HandlePrimaryError(operation string, err error) error
	HandleSecondaryError(operation string, err error)
}

// DefaultErrorHandler provides default error handling
type DefaultErrorHandler struct{}

func (h *DefaultErrorHandler) HandlePrimaryError(operation string, err error) error {
	return err
}

func (h *DefaultErrorHandler) HandleSecondaryError(operation string, err error) {
	// Log secondary errors but don't fail the operation
	log.Warn().Err(err).Str("operation", operation).Msg("Secondary store operation failed")
}

func SetupStoreManager(primaryType, secondaryType string) (DualStore, error) {
	if primaryType == "" {
		return nil, ErrUnknownStoreType
	}

	// Create primary store
	primary, err := createStore(primaryType)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"primaryType":   primaryType,
			"secondaryType": secondaryType,
		}).Err(err).Msg("Could not create store manager!")
		return nil, err
	}

	// Create secondary store
	var secondary Store
	if secondaryType != "" && secondaryType != primaryType {
		secondary, err = createStore(secondaryType)
		if err != nil {
			log.Fatal().Fields(map[string]any{
				"primaryType":   primaryType,
				"secondaryType": secondaryType,
			}).Err(err).Msg("Could not create store manager!")
			return nil, err
		}
	}

	// Create and return the DualStoreManager
	manager := &DualStoreManager{
		primary:      primary,
		secondary:    secondary,
		mu:           sync.RWMutex{},
		errorHandler: new(DefaultErrorHandler),
	}

	manager.Initialize()
	return manager, nil
}

func (m *DualStoreManager) Initialize() {

	if m.primary != nil {
		m.primary.Initialize()
	}
	if m.secondary != nil {
		m.secondary.Initialize()
	}
}

func (m *DualStoreManager) InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.ResourceConfiguration) {

	if m.primary != nil {
		m.primary.InitializeResource(kubernetesClient, resourceConfig)
	}
	if m.secondary != nil {
		m.secondary.InitializeResource(kubernetesClient, resourceConfig)
	}
}

func (m *DualStoreManager) Create(obj *unstructured.Unstructured) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var primaryErr error

	if m.primary != nil {
		if primaryErr = m.primary.Create(obj); primaryErr != nil {
			primaryErr = m.errorHandler.HandlePrimaryError("Create", primaryErr)
		}
	}
	if m.secondary != nil {
		go func() {
			if secondaryErr := m.secondary.Create(obj); secondaryErr != nil {
				m.errorHandler.HandleSecondaryError("Create", secondaryErr)
			}
		}()
	}

	return primaryErr
}

func (m *DualStoreManager) Update(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var primaryErr error

	if m.primary != nil {
		if primaryErr = m.primary.Update(oldObj, newObj); primaryErr != nil {
			primaryErr = m.errorHandler.HandlePrimaryError("Update", primaryErr)
		}
	}

	if m.secondary != nil {
		go func() {
			if secondaryErr := m.secondary.Update(oldObj, newObj); secondaryErr != nil {
				m.errorHandler.HandleSecondaryError("Update", secondaryErr)
			}
		}()
	}
	return primaryErr
}

func (m *DualStoreManager) Delete(obj *unstructured.Unstructured) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var primaryErr error

	if primaryErr = m.primary.Delete(obj); primaryErr != nil {
		primaryErr = m.errorHandler.HandlePrimaryError("Delete", primaryErr)
	}

	if m.secondary != nil {
		go func() {
			if secondaryErr := m.secondary.Delete(obj); secondaryErr != nil {
				m.errorHandler.HandleSecondaryError("Delete", secondaryErr)
			}
		}()
	}

	return primaryErr
}

func (m *DualStoreManager) Count(dataset string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.primary != nil && m.primary.Connected() {
		return m.primary.Count(dataset)
	}

	return 0, ErrNoConnectedStore
}

func (m *DualStoreManager) Keys(dataset string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.primary != nil && m.primary.Connected() {
		return m.primary.Keys(dataset)
	}

	return nil, ErrNoConnectedStore
}

func (m *DualStoreManager) Read(dataset string, name string) (*unstructured.Unstructured, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.primary != nil && m.primary.Connected() {
		return m.primary.Read(dataset, name)
	}

	return nil, ErrNoConnectedStore
}

func (m *DualStoreManager) List(dataset string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.primary != nil && m.primary.Connected() {
		return m.primary.List(dataset, fieldSelector, limit)
	}

	return nil, ErrNoConnectedStore
}

func (m *DualStoreManager) Shutdown() {
	if m.primary != nil {
		m.primary.Shutdown()
	}
	if m.secondary != nil {
		m.secondary.Shutdown()
	}
}

func (m *DualStoreManager) Connected() bool {
	if m.primary != nil && m.primary.Connected() {
		return true
	}
	return false
}

func (m *DualStoreManager) GetPrimary() Store {
	return m.primary
}

func (m *DualStoreManager) GetSecondary() Store {
	return m.secondary
}
