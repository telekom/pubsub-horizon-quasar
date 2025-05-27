// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

func GetLabelsForResource(obj *unstructured.Unstructured, resourceConfig *config.ResourceConfiguration) prometheus.Labels {
	var labels = make(prometheus.Labels)

	for labelName, labelValue := range resourceConfig.Prometheus.Labels {
		var val = labelValue
		if strings.HasPrefix(labelValue, "$") {
			var ok bool
			val, ok, _ = unstructured.NestedString(obj.Object, strings.Split(labelValue[1:], ".")...)
			if !ok {
				var gvr = resourceConfig.GetGroupVersionResource()
				log.Warn().
					Fields(CreateFieldForResource(&gvr)).
					Msgf("Could not resolve nested path '%s' for label %s", labelValue, labelName)
				continue
			}
		}

		labels[labelName] = val
	}

	return labels
}
