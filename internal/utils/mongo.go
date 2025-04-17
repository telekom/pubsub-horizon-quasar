// Copyright 2025 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
)

func GetMongoId(obj *unstructured.Unstructured) string {
	resourceConfig, ok := config.Current.GetResourceConfiguration(obj)
	if ok {
		fieldPath := strings.Split(strings.TrimPrefix(resourceConfig.MongoId, "."), ".")
		val, ok, _ := unstructured.NestedString(obj.Object, fieldPath...)
		if ok {
			return val
		}
	}

	return string(obj.GetUID())
}
