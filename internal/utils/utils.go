// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"fmt"
	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"strings"
)

func GetFieldsOfObject(obj *unstructured.Unstructured) map[string]any {
	return map[string]any{
		"name":      obj.GetName(),
		"namespace": obj.GetNamespace(),
		"uid":       obj.GetUID(),
	}
}

func CreateFieldsForOp(operation string, obj *unstructured.Unstructured) map[string]any {
	var objFields = GetFieldsOfObject(obj)
	objFields["operation"] = operation
	return objFields
}

func CreateFieldForResource(resource *schema.GroupVersionResource) map[string]any {
	return map[string]any{
		"group":    resource.Group,
		"resource": resource.Resource,
		"version":  resource.Version,
	}
}

func AddMissingEnvironment(obj *unstructured.Unstructured) {
	var raw = obj.UnstructuredContent()
	_, ok, err := unstructured.NestedString(raw, "spec", "environment")
	if err != nil {
		log.Warn().Fields(GetFieldsOfObject(obj)).Err(err).Msg("Environment is not a string (spec.environment)")
		return
	}

	if !ok {
		if err := unstructured.SetNestedField(raw, "default", "spec", "environment"); err != nil {
			log.Warn().Fields(GetFieldsOfObject(obj)).Err(err).Msg("Could not modify environment (spec.environment)")
		}
	}

	obj.SetUnstructuredContent(raw)
}

func AsAnySlice(args []string) []any {
	var slice = make([]any, len(args))
	for i, arg := range args {
		slice[i] = arg
	}
	return slice
}

func GetGroupVersionId(obj *unstructured.Unstructured) string {
	var gvk = obj.GroupVersionKind()
	return strings.ToLower(fmt.Sprintf("%ss.%s.%s", gvk.Kind, gvk.Group, gvk.Version))
}
