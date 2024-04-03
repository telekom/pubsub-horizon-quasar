package main

import (
	"github.com/telekom/quasar/internal/cmd"
	"github.com/telekom/quasar/internal/config"
)

func main() {
	_ = config.Current
	cmd.Execute()
}
