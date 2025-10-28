// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package provisioning

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/store"
)

func syncPrimaryToSecondaryWithContext(ctx context.Context, dualStore store.DualStore) error {
	logger := log.With().Str("operation", "syncMongoToHazelcast").Logger()

	syncStartTime := time.Now()

	logger.Info().Msg("Starting MongoDB to Hazelcast synchronization")

	select {
	case <-ctx.Done():
		err := ctx.Err()
		logger.Warn().Err(err).Msg("Synchronization cancelled before starting")
		return err
	default:
	}

	mongoStore, hazelcastStore := getMongoAndHazelcastStores(dualStore)
	logStoreIdentification(mongoStore, hazelcastStore)

	if mongoStore == nil || hazelcastStore == nil {
		err := fmt.Errorf("mongoDB or Hazelcast store not found in configuration")
		logger.Error().Err(err).Msg("Synchronization failed")
		return err
	}

	if !mongoStore.Connected() {
		err := fmt.Errorf("mongoDB store is not connected")
		logger.Error().Err(err).Msg("Synchronization failed")
		return err
	}

	if !hazelcastStore.Connected() {
		err := fmt.Errorf("hazelcast store is not connected")
		logger.Error().Err(err).Msg("Synchronization failed")
		return err
	}

	totalResources := 0
	totalDocuments := 0
	successfulDocuments := 0
	failedDocuments := 0

	for _, resourceConfig := range config.Current.Resources {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			logger.Warn().Err(err).
				Int("completedResources", totalResources).
				Int("totalDocuments", totalDocuments).
				Int("successfulDocuments", successfulDocuments).
				Int("failedDocuments", failedDocuments).
				Msg("Synchronization cancelled during execution")
			return err
		default:
		}

		cacheName := resourceConfig.GetCacheName()
		logger := logger.With().Str("dataset", cacheName).Logger()

		logger.Info().Msg("Synchronizing resource")
		totalResources++

		objects, err := mongoStore.List(cacheName, "", 0)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to load data from MongoDB")
			continue
		}

		resourceCount := len(objects)
		totalDocuments += resourceCount
		logger.Info().Int("count", resourceCount).Msg("Data loaded from MongoDB")

		resourceSuccess := 0
		resourceErrors := 0

		for i := range objects {
			select {
			case <-ctx.Done():
				err := ctx.Err()
				logger.Warn().Err(err).
					Int("currentResource", totalResources).
					Int("processedItems", i).
					Int("totalItems", resourceCount).
					Msg("Synchronization cancelled during processing items")
				return err
			default:
			}

			if err := hazelcastStore.Create(&objects[i]); err != nil {
				resourceErrors++
				failedDocuments++
				logger.Error().Err(err).
					Str("name", objects[i].GetName()).
					Msg("Failed to sync object to Hazelcast")
			} else {
				resourceSuccess++
				successfulDocuments++
			}
		}

		logger.Info().
			Int("success", resourceSuccess).
			Int("errors", resourceErrors).
			Msg("Resource synchronization completed")
	}

	syncDuration := time.Since(syncStartTime)

	logger.Info().
		Int("totalResources", totalResources).
		Int("totalDocuments", totalDocuments).
		Int("successfulDocuments", successfulDocuments).
		Int("failedDocuments", failedDocuments).
		Str("duration", syncDuration.String()).
		Msg("MongoDB to Hazelcast synchronization completed")

	if failedDocuments > 0 {
		failureRate := float64(failedDocuments) / float64(totalDocuments) * 100
		logger.Warn().
			Int("failedDocuments", failedDocuments).
			Int("totalDocuments", totalDocuments).
			Float64("failureRate", failureRate).
			Msg("Some documents failed to synchronize, but synchronization completed. Cache may have partial data.")
	}
	return nil
}
