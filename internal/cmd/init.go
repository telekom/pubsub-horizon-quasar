// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"errors"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a local configuration for testing",
	Run:   initializeConfig,
}

func initializeConfig(*cobra.Command, []string) {
	if err := viper.SafeWriteConfig(); err != nil {
		handleConfigInitErr(err)
		return
	}
	log.Info().Msg("Configuration initialized")
}

func handleConfigInitErr(err error) {
	var configFileAlreadyExistsError viper.ConfigFileAlreadyExistsError
	if errors.As(err, &configFileAlreadyExistsError) {
		log.Error().Msg("Configuration file already exists")
	}
}
