// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/fallback"
	"github.com/telekom/quasar/internal/k8s"
	"github.com/telekom/quasar/internal/metrics"
	"github.com/telekom/quasar/internal/provisioning"
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

		if err := validateMode(); err != nil {
			log.Fatal().Err(err).Msg("Invalid mode configuration")
		}

		switch config.Current.Mode {
		case config.ModeProvisioning:
			go provisioning.Listen(config.Current.Provisioning.Port)
		case config.ModeWatcher:
			k8s.SetupWatcherStore()
			utils.RegisterShutdownHook(k8s.WatcherStore.Shutdown, 1)

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

			for _, resourceConfig := range config.Current.Resources {
				watcher, err := k8s.NewResourceWatcher(kubernetesClient, &resourceConfig, config.Current.ReSyncPeriod)
				if err != nil {
					log.Fatal().Err(err).Msg("Could not create resource watcher!")
				}
				go watcher.Start()
				utils.RegisterShutdownHook(watcher.Stop, 0)
			}
		}

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

func validateMode() error {
	if config.Current.Mode != config.ModeProvisioning && config.Current.Mode != config.ModeWatcher {
		return fmt.Errorf("invalid mode %q: must be 'provisioning' or 'watcher'", config.Current.Mode)
	}
	return nil
}
