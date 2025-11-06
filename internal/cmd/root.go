// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package cmd

import "github.com/spf13/cobra"

var rootCmd = &cobra.Command{
	Use:   "quasar",
	Short: "Quasar is a tiny service for synchronizing the state of custom resources with caches or databases.",
}

func init() {
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.AddCommand(initCmd, runCmd)
}
