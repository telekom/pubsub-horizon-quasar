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
	"github.com/telekom/quasar/internal/utils"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Starts synchronizing resources with the configured data store",
	Run: func(cmd *cobra.Command, args []string) {
		kubeConfigPath, _ := cmd.Flags().GetString("kubeconfig")

		switch config.Current.Mode {

		case config.ModeProvisioning:
			go provisioning.Listen(config.Current.Provisioning.Port)

		case config.ModeWatcher:
			k8s.SetupWatchers(kubeConfigPath)

		default:
			err := fmt.Errorf("invalid mode %q: must be 'provisioning' or 'watcher'", config.Current.Mode)
			log.Fatal().Err(err).Msg("Invalid mode configuration")

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
