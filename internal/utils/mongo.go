// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"strings"

	"github.com/telekom/quasar/internal/config"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func GetMongoId(obj *unstructured.Unstructured) (string, error) {
	resourceConfig, ok := config.Current.GetResourceConfiguration(obj)
	if ok {
		mongoIdField := resourceConfig.MongoId
		if mongoIdField != "" {
			fieldPath := strings.Split(strings.TrimPrefix(mongoIdField, "."), ".")
			val, ok, _ := unstructured.NestedString(obj.Object, fieldPath...)
			if ok {
				return val, nil
			}
			return "", fmt.Errorf("could not determine field '%s' for resource with uid %s", mongoIdField, string(obj.GetUID()))
		}
	}

	return string(obj.GetUID()), nil
}
