// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package fallback

import (
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
)

var CurrentFallback Fallback

type ReplayFunc func(obj *unstructured.Unstructured) error

type Fallback interface {
	Initialize()
	ReplayResource(gvr *schema.GroupVersionResource, replayFunc ReplayFunc) (int64, error)
}

func SetupFallback() {
	var fallbackType = config.Current.Fallback.Type
	var err error
	CurrentFallback, err = createFallback(fallbackType)
	if err != nil {
		log.Fatal().Fields(map[string]any{
			"fallbackType": fallbackType,
		}).Err(err).Msg("Could not create fallback!")
	}
}

func createFallback(fallbackType string) (Fallback, error) {
	switch strings.ToLower(fallbackType) {

	case "mongo":
		return new(MongoFallback), nil

	default:
		return nil, ErrUnknownFallback

	}
}
