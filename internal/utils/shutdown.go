// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"cmp"
	"os"
	"os/signal"
	"slices"
	"syscall"

	"github.com/rs/zerolog/log"
)

var shutdownHooks []ShutdownHook

type ShutdownHook struct {
	Priority int
	Func     ShutdownFunc
}

type ShutdownFunc func()

func init() {
	shutdownHooks = make([]ShutdownHook, 0)
}

func RegisterShutdownHook(shutdownFunc ShutdownFunc, priority int) {
	shutdownHooks = append(shutdownHooks, ShutdownHook{priority, shutdownFunc})
}

func GracefulShutdown() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	slices.SortFunc(shutdownHooks, func(a, b ShutdownHook) int {
		return cmp.Compare(a.Priority, b.Priority)
	})

	log.Info().Msg("Shutting down...")
	for _, hook := range shutdownHooks {
		hook.Func()
	}

	os.Exit(0)
}
