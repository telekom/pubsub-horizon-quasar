package state

import (
	"encoding/base64"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

type Head struct {
	HistoryEntry
	ResumeTokenStr string         `json:"resumeToken"`
	History        []HistoryEntry `json:"history"`
}

type HistoryEntry struct {
	ID            string    `json:"id"`
	DocumentCount int64     `json:"documentCount"`
	Created       time.Time `json:"created"`
}

func (h *Head) ResumeToken() (bson.Raw, error) {
	bytes, err := base64.StdEncoding.DecodeString(h.ResumeTokenStr)
	if err != nil {
		return nil, err
	}

	return bytes, nil
}
