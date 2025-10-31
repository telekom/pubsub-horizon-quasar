// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/test"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGetDatasetForGvr(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	tests := []struct {
		name     string
		gvr      schema.GroupVersionResource
		expected string
	}{
		{
			name: "standard GVR",
			gvr: schema.GroupVersionResource{
				Group:    "subscriber.horizon.telekom.de",
				Version:  "v1",
				Resource: "subscriptions",
			},
			expected: "subscriptions.subscriber.horizon.telekom.de.v1",
		},
		{
			name: "core group (empty group)",
			gvr: schema.GroupVersionResource{
				Group:    "",
				Version:  "v1",
				Resource: "pods",
			},
			expected: "pods..v1",
		},
		{
			name: "custom resource",
			gvr: schema.GroupVersionResource{
				Group:    "mygroup.example.com",
				Version:  "v1beta1",
				Resource: "myresources",
			},
			expected: "myresources.mygroup.example.com.v1beta1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getDataSetForGvr(tt.gvr)
			assertions.Equal(tt.expected, result)
		})
	}

	errorCount := test.LogRecorder.GetRecordCount(zerolog.ErrorLevel)
	assertions.Equal(0, errorCount, "no errors should be logged")
}
