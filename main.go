// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/telekom/quasar/internal/cmd"
	"github.com/telekom/quasar/internal/config"
)

func main() {
	_ = config.Current
	cmd.Execute()
}
