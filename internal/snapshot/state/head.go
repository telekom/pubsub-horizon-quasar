// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package state

import (
	"time"
)

type Head struct {
	HistoryEntry
	ResumeToken *ResumeToken   `json:"resumeToken"`
	History     []HistoryEntry `json:"history"`
}

type HistoryEntry struct {
	ID            string    `json:"snapshotId"`
	DocumentCount int64     `json:"documentCount"`
	Created       time.Time `json:"created"`
}

func (h *Head) GetSnapshotIds() []string {
	snapshotIds := make([]string, 0)
	snapshotIds = append(snapshotIds, h.ID)

	for _, historicEntry := range h.History {
		snapshotIds = append(snapshotIds, historicEntry.ID)
	}

	return snapshotIds
}
