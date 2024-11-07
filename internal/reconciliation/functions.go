// Copyright 2024 Deutsche Telekom IT GmbH
//
// SPDX-License-Identifier: Apache-2.0

package reconciliation

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

type Functions struct {
	AddFunc   func(obj *unstructured.Unstructured)
	CountFunc func(mapName string) (int, error)
	KeysFunc  func(mapName string) ([]string, error)
}
