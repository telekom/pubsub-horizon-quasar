// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/fallback"
	"github.com/telekom/quasar/internal/k8s"
	"github.com/telekom/quasar/internal/metrics"
	"github.com/telekom/quasar/internal/provisioning"
	"github.com/telekom/quasar/internal/snapshot"
	"github.com/telekom/quasar/internal/snapshot/mongo"
	"github.com/telekom/quasar/internal/snapshot/zoo"
	"github.com/telekom/quasar/internal/utils"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Starts synchronizing resources with the configured data store",
	Run: func(cmd *cobra.Command, args []string) {
		kubeConfigPath, _ := cmd.Flags().GetString("kubeconfig")

		var startMode func()
		switch config.Current.Mode {
		case config.ModeProvisioning:
			startMode = func() { go provisioning.Listen(config.Current.Provisioning.Port) }
		case config.ModeWatcher:
			startMode = func() { k8s.SetupWatchers(kubeConfigPath) }
		default:
			err := fmt.Errorf("invalid mode %q: must be 'provisioning' or 'watcher'", config.Current.Mode)
			log.Fatal().Err(err).Msg("Invalid mode configuration")
		}

		if snapshotConfig := config.Current.Snapshot; snapshotConfig.Enabled {
			startSnapshotService(&snapshotConfig)
		}

		startMode()

		if config.Current.Metrics.Enabled {
			go metrics.ExposeMetrics()
		}

		if strings.ToLower(config.Current.Fallback.Type) != "none" {
			fallback.SetupFallback()
		} else {
			log.Warn().Msg("No fallback is configured. Quasar won't be able to restore data if the kubernetes api fails")
		}
		utils.GracefulShutdown()
	},
}

func init() {
	runCmd.Flags().StringP("kubeconfig", "k", "", "sets the kubeconfig that should be used (service account will be used if unset)")
}

func startSnapshotService(snapshotConfig *config.Snapshot) {
	mongoClient, err := mongo.NewClient(
		snapshotConfig.Mongo.Uri,
		snapshotConfig.Mongo.Database,
		snapshotConfig.Mongo.SourceCollection,
		snapshotConfig.Mongo.TargetCollection,
		snapshotConfig.Mongo.ReconnectInterval,
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create snapshot mongo client")
	}

	zooClient, err := zoo.NewClient(snapshotConfig.ZooKeeper.Addresses, snapshotConfig.ZooKeeper.SessionTimeout)
	if err != nil {
		log.Fatal().Err(err).Msg("Could not create snapshot zookeeper client")
	}

	snapshotService := snapshot.NewService(
		mongoClient,
		zooClient,
		snapshotConfig.RebuildInterval,
		snapshotConfig.ActivationDelay,
		snapshotConfig.CleanupInterval,
	)
	if err := snapshotService.Start(); err != nil {
		log.Fatal().Err(err).Msg("Could not start snapshot service")
	}

	utils.RegisterShutdownHook(func() {
		snapshotService.Stop()
		mongoClient.Close()
		zooClient.Disconnect()
	}, 0)
}
