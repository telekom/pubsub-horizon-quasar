// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

func CreateInClusterClient() (*dynamic.DynamicClient, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func CreateKubeConfigClient(kubeConfigPath string) (*dynamic.DynamicClient, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		return nil, err
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return client, nil
}

func createInformer(client dynamic.Interface, resource schema.GroupVersionResource, namespace string, reSyncPeriod time.Duration) cache.SharedIndexInformer {
	var factory = dynamicinformer.NewFilteredDynamicSharedInformerFactory(client, reSyncPeriod, namespace, nil)
	return factory.ForResource(resource).Informer()
}
