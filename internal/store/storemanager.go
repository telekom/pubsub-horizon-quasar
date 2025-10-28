// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"sync"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	reconciler "github.com/telekom/quasar/internal/reconciliation"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type DualStore interface {
	Store
	GetPrimary() Store
	GetSecondary() Store
}

// DualStoreManager handles primary and secondary store
type DualStoreManager struct {
	managerId     string
	primary       Store
	secondary     Store
	primaryType   string
	secondaryType string
	mu            sync.RWMutex
	logger        zerolog.Logger
}

func SetupDualStoreManager(id string, primaryType, secondaryType string) (DualStore, error) {
	if primaryType == "" {
		return nil, ErrUnknownStoreType
	}

	// Create structured logger with context
	logger := log.With().
		Str("component", "DualStoreManager").
		Str("id", id).
		Str("primaryType", primaryType).
		Str("secondaryType", secondaryType).
		Logger()

	// Create primary store
	primary, err := createStore(primaryType)
	if err != nil {
		logger.Fatal().Err(err).
			Msg("Could not create primary store!")
		return nil, err
	}

	// Create secondary store
	var secondary Store
	if secondaryType != "" && secondaryType != primaryType {
		secondary, err = createStore(secondaryType)
		if err != nil {
			logger.Fatal().Err(err).
				Msg("Could not create secondary store!")
			return nil, err
		}
	}

	// Create and return the DualStoreManager
	manager := &DualStoreManager{
		managerId:     id,
		primary:       primary,
		secondary:     secondary,
		primaryType:   primaryType,
		secondaryType: secondaryType,
		mu:            sync.RWMutex{},
		logger:        logger,
	}

	manager.Initialize()
	logger.Debug().Msg("Successfully created dual store manager")
	return manager, nil
}

func (m *DualStoreManager) Initialize() {
	m.primary.Initialize()

	if m.secondary != nil {
		m.secondary.Initialize()
	}
}

func (m *DualStoreManager) InitializeResource(reconciliation *reconciler.Reconciliation, resourceConfig *config.Resource) {
	m.primary.InitializeResource(reconciliation, resourceConfig)

	if m.secondary != nil {
		m.secondary.InitializeResource(reconciliation, resourceConfig)
	}
}

func (m *DualStoreManager) Create(obj *unstructured.Unstructured) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var primaryErr error
	if primaryErr := m.primary.Create(obj); primaryErr != nil {
		m.logPrimaryError("Create", primaryErr)
	}

	if m.secondary != nil {
		go func() {
			if secondaryErr := m.secondary.Create(obj); secondaryErr != nil {
				m.logSecondaryError("Create", secondaryErr)
			}
		}()
	}
	return primaryErr
}

func (m *DualStoreManager) Update(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var primaryErr error

	if primaryErr = m.primary.Update(oldObj, newObj); primaryErr != nil {
		m.logPrimaryError("Update", primaryErr)
	}

	if m.secondary != nil {
		go func() {
			if secondaryErr := m.secondary.Update(oldObj, newObj); secondaryErr != nil {
				m.logSecondaryError("Update", secondaryErr)
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
		m.logPrimaryError("Update", primaryErr)
	}

	if m.secondary != nil {
		go func() {
			if secondaryErr := m.secondary.Delete(obj); secondaryErr != nil {
				m.logSecondaryError("Update", secondaryErr)
			}
		}()
	}

	return primaryErr
}

func (m *DualStoreManager) Count(dataset string) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.primary.Count(dataset)
}

func (m *DualStoreManager) Keys(dataset string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.primary.Keys(dataset)
}

func (m *DualStoreManager) Read(dataset string, name string) (*unstructured.Unstructured, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.primary.Read(dataset, name)
}

func (m *DualStoreManager) List(dataset string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.primary.List(dataset, fieldSelector, limit)
}

func (m *DualStoreManager) Shutdown() {
	m.primary.Shutdown()

	if m.secondary != nil {
		m.secondary.Shutdown()
	}
}

func (m *DualStoreManager) Connected() bool {
	if m.primary.Connected() {
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

func (m *DualStoreManager) logPrimaryError(operation string, err error) {
	m.logger.Warn().Err(err).Str("operation", operation).Msg("Primary store operation failed")
}

func (m *DualStoreManager) logSecondaryError(operation string, err error) {
	m.logger.Warn().Err(err).Str("operation", operation).Msg("Secondary store operation failed")
}
