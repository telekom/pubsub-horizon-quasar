package cmd

import (
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/k8s"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/utils"
	"k8s.io/client-go/dynamic"
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

		store.SetupStore()

		for _, resourceConfig := range config.Current.Resources {
			watcher, err := k8s.NewResourceWatcher(kubernetesClient, &resourceConfig, config.Current.ReSyncPeriod)
			if err != nil {
				log.Fatal().Err(err).Msg("Could not create resource watcher!")
			}
			go watcher.Start()
		}

		utils.WaitForExit()
	},
}

func init() {
	runCmd.Flags().StringP("kubeconfig", "k", "", "sets the kubeconfig that should be used (service account will be used if unset)")
}
