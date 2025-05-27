// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/fallback"
	"github.com/telekom/quasar/internal/k8s"
	"github.com/telekom/quasar/internal/metrics"
	"github.com/telekom/quasar/internal/provisioning"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/client-go/dynamic"
	"strings"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Starts synchronizing resources with the configured data store",
	Run: func(cmd *cobra.Command, args []string) {
		var kubernetesClient *dynamic.DynamicClient
		var kubeConfigPath, _ = cmd.Flags().GetString("kubeconfig")
		var err error

		if useServiceAccount := len(kubeConfigPath) == 0; useServiceAccount {
			kubernetesClient, err = k8s.CreateInClusterClient()
			if err != nil {
				log.Fatal().Err(err).Msg("Could not create kubernetes client!")
			}
		} else {
			kubernetesClient, err = k8s.CreateKubeConfigClient(kubeConfigPath)
			if err != nil {
				log.Fatal().Err(err).Msg("Could not create kubernetes client!")
			}
		}

		if config.Current.Provisioning.Enabled {
			go provisioning.Listen(config.Current.Provisioning.Port)
		}

		if config.Current.Metrics.Enabled {
			go metrics.ExposeMetrics()
		}

		if strings.ToLower(config.Current.Fallback.Type) != "none" {
			fallback.SetupFallback()
		} else {
			log.Warn().Msg("No fallback is configured. Quasar won't be able to restore data if the kubernetes api fails")
		}

		store.SetupStore()
		utils.RegisterShutdownHook(store.CurrentStore.Shutdown, 1)

		for _, resourceConfig := range config.Current.Resources {
			watcher, err := k8s.NewResourceWatcher(kubernetesClient, &resourceConfig, config.Current.ReSyncPeriod)
			if err != nil {
				log.Fatal().Err(err).Msg("Could not create resource watcher!")
			}
			go watcher.Start()
			utils.RegisterShutdownHook(watcher.Stop, 0)
		}

		utils.GracefulShutdown()
	},
}

func init() {
	runCmd.Flags().StringP("kubeconfig", "k", "", "sets the kubeconfig that should be used (service account will be used if unset)")
}
