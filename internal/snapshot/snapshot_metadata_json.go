// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

func encodeDescriptor(value descriptor) ([]byte, error) {
	if err := validateDescriptor(value); err != nil {
		return nil, err
	}
	value.CreatedAt = value.CreatedAt.UTC()
	return json.Marshal(value)
}

func decodeJSONDescriptor(data []byte) (descriptor, error) {
	fields, err := descriptorFields(data)
	if err != nil {
		return descriptor{}, err
	}
	var result descriptor
	var timestamp string
	for key, target := range map[string]any{
		fieldSnapshotID: &result.SnapshotID, fieldSourceHash: &result.SourceHash,
		fieldDocumentCount: &result.DocumentCount, fieldCreatedAt: &timestamp,
	} {
		raw, found := fields[key]
		if !found || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return descriptor{}, errors.New("ZooKeeper descriptor has missing or null fields")
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return descriptor{}, errors.New("ZooKeeper descriptor has invalid field types")
		}
	}
	result.CreatedAt, err = descriptorTimestamp(timestamp)
	if err != nil {
		return descriptor{}, err
	}
	return result, validateDescriptor(result)
}

func descriptorFields(data []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("ZooKeeper descriptor must be a single JSON object")
	}
	fields := make(map[string]json.RawMessage, 4)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, errors.New("ZooKeeper descriptor contains invalid JSON")
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("ZooKeeper descriptor contains an invalid field")
		}
		switch key {
		case fieldSnapshotID, fieldSourceHash, fieldDocumentCount, fieldCreatedAt:
		default:
			return nil, errors.New("ZooKeeper descriptor contains an unknown field")
		}
		if _, found := fields[key]; found {
			return nil, errors.New("ZooKeeper descriptor contains a duplicate field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.New("ZooKeeper descriptor contains invalid JSON")
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errors.New("ZooKeeper descriptor contains invalid JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("ZooKeeper descriptor must not contain trailing data")
	}
	return fields, nil
}

func descriptorTimestamp(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") {
		return time.Time{}, errors.New("ZooKeeper createdAt must use UTC with a trailing Z")
	}
	seconds, fraction, fractional := strings.Cut(strings.TrimSuffix(value, "Z"), ".")
	if fractional && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return time.Time{}, errors.New("ZooKeeper createdAt must not contain non-zero fractional seconds")
	}
	canonical := seconds + "Z"
	result, err := time.Parse(time.RFC3339, canonical)
	if err != nil || result.Format(time.RFC3339) != canonical {
		return time.Time{}, errors.New("ZooKeeper createdAt must be an RFC3339 UTC timestamp")
	}
	return result, nil
}
