// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"os"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var LogRecorder *LogRecorderHook

func InstallLogRecorder() {
	if LogRecorder == nil {
		LogRecorder = &LogRecorderHook{
			records: make(map[zerolog.Level]int),
		}
		log.Logger = log.Logger.Hook(LogRecorder).Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}
}

type LogRecorderHook struct {
	records map[zerolog.Level]int
}

func (h *LogRecorderHook) Run(_ *zerolog.Event, level zerolog.Level, _ string) {
	h.record(level)
}

func (h *LogRecorderHook) Reset() {
	h.records = make(map[zerolog.Level]int)
}

func (h *LogRecorderHook) GetRecordCount(levels ...zerolog.Level) int {
	var count = 0
	for _, level := range levels {
		count += h.records[level]
	}
	return count
}

func (h *LogRecorderHook) record(level zerolog.Level) {
	h.records[level]++
}
