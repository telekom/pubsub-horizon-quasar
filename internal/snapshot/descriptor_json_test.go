// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

func TestDescriptorJSONContract(t *testing.T) {
	version := testDescriptor(time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC), 0)
	data, err := encodeDescriptor(version)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fields))
	require.Len(t, fields, 4)
	require.JSONEq(t, `0`, string(fields["documentCount"]))
	require.JSONEq(t, `"2026-09-30T15:00:00Z"`, string(fields["createdAt"]))
	decoded, err := decodeJSONDescriptor(data)
	require.NoError(t, err)
	require.True(t, sameDescriptor(version, decoded))
	raw, err := bson.Marshal(version)
	require.NoError(t, err)
	decoded, err = decodeDescriptor(bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: raw})
	require.NoError(t, err)
	require.True(t, sameDescriptor(version, decoded))

	tests := []struct {
		name   string
		mutate func(map[string]json.RawMessage)
		valid  bool
	}{
		{"zero fraction", func(m map[string]json.RawMessage) { m["createdAt"] = json.RawMessage(`"2026-09-30T15:00:00.000Z"`) }, true},
		{"many zero digits", func(m map[string]json.RawMessage) {
			m["createdAt"] = json.RawMessage(`"2026-09-30T15:00:00.000000000000Z"`)
		}, true},
		{"non-zero fraction", func(m map[string]json.RawMessage) { m["createdAt"] = json.RawMessage(`"2026-09-30T15:00:00.123Z"`) }, false},
		{"sub-nanosecond fraction", func(m map[string]json.RawMessage) {
			m["createdAt"] = json.RawMessage(`"2026-09-30T15:00:00.000000000001Z"`)
		}, false},
		{"offset", func(m map[string]json.RawMessage) { m["createdAt"] = json.RawMessage(`"2026-09-30T15:00:00+00:00"`) }, false},
		{"wrong second", func(m map[string]json.RawMessage) { m["createdAt"] = json.RawMessage(`"2026-09-30T15:00:01Z"`) }, false},
		{"comma fraction", func(m map[string]json.RawMessage) { m["createdAt"] = json.RawMessage(`"2026-09-30T15:00:00,000Z"`) }, false},
		{"short hour", func(m map[string]json.RawMessage) { m["createdAt"] = json.RawMessage(`"2026-09-30T5:00:00Z"`) }, false},
		{"missing count", func(m map[string]json.RawMessage) { delete(m, "documentCount") }, false},
		{"null count", func(m map[string]json.RawMessage) { m["documentCount"] = json.RawMessage(`null`) }, false},
		{"string count", func(m map[string]json.RawMessage) { m["documentCount"] = json.RawMessage(`"0"`) }, false},
		{"fractional count", func(m map[string]json.RawMessage) { m["documentCount"] = json.RawMessage(`0.0`) }, false},
		{"negative count", func(m map[string]json.RawMessage) { m["documentCount"] = json.RawMessage(`-1`) }, false},
		{"overflow count", func(m map[string]json.RawMessage) { m["documentCount"] = json.RawMessage(`9223372036854775808`) }, false},
		{"unknown field", func(m map[string]json.RawMessage) { m["recentSnapshots"] = json.RawMessage(`[]`) }, false},
		{"null time", func(m map[string]json.RawMessage) { m["createdAt"] = json.RawMessage(`null`) }, false},
		{"uppercase hash", func(m map[string]json.RawMessage) {
			m["sourceHash"] = json.RawMessage(`"` + strings.Repeat("A", 64) + `"`)
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			copyFields := make(map[string]json.RawMessage, len(fields))
			for key, value := range fields {
				copyFields[key] = value
			}
			tt.mutate(copyFields)
			payload, err := json.Marshal(copyFields)
			require.NoError(t, err)
			_, err = decodeJSONDescriptor(payload)
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	for _, payload := range []string{
		"null", "[]", string(data) + string(data), string(data) + " trailing",
		strings.TrimSuffix(string(data), "}") + `,"documentCount":0}`,
	} {
		_, err := decodeJSONDescriptor([]byte(payload))
		require.Error(t, err, payload)
	}
	version.CreatedAt = version.CreatedAt.Add(time.Millisecond)
	_, err = encodeDescriptor(version)
	require.Error(t, err)
}
