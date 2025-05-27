// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/test"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/fake"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	watcher       *ResourceWatcher
	fakeClient    dynamic.Interface
	subscriptions []*unstructured.Unstructured
)

func TestMain(m *testing.M) {
	test.InstallLogRecorder()
	subscriptions = test.ReadTestSubscriptions("../../testdata/subscriptions.json")

	config.Current = buildTestConfig()
	store.CurrentStore = new(test.DummyStore)

	fakeClient = createFakeClient()

	code := m.Run()
	os.Exit(code)
}

func buildTestConfig() *config.Configuration {
	var testConfig = new(config.Configuration)
	testConfig.Store.Type = "dummy"

	var testResourceConfig = config.ResourceConfiguration{}
	testResourceConfig.Kubernetes.Group = "subscriber.horizon.telekom.de"
	testResourceConfig.Kubernetes.Version = "v1"
	testResourceConfig.Kubernetes.Resource = "subscriptions"
	testResourceConfig.Kubernetes.Namespace = "playground"

	testConfig.Resources = []config.ResourceConfiguration{testResourceConfig}

	return testConfig
}

func createFakeClient() *fake.FakeDynamicClient {
	var scheme = runtime.NewScheme()
	return fake.NewSimpleDynamicClient(scheme, subscriptions[0], subscriptions[1])
}

func processSubscriptions(action string) {
	var ctx = context.Background()
	var gvr = config.Current.Resources[0].GetGroupVersionResource()
	var resource = fakeClient.Resource(gvr).Namespace("playground")

	for _, subscription := range subscriptions {
		switch strings.ToLower(action) {

		case "add":
			_, _ = resource.Create(ctx, subscription, v1.CreateOptions{})

		case "update":
			resourceVersion, _ := strconv.Atoi(subscription.GetResourceVersion())
			resourceVersion++

			subscription.SetResourceVersion(fmt.Sprintf("%d", resourceVersion))
			_, _ = resource.Update(ctx, subscription, v1.UpdateOptions{})

		case "delete":
			_ = resource.Delete(ctx, subscription.GetName(), v1.DeleteOptions{})

		}
	}
}

func TestNewResourceWatcher(t *testing.T) {
	var assertions = assert.New(t)
	var err error
	watcher, err = NewResourceWatcher(fakeClient, &config.Current.Resources[0], 30*time.Second)
	assertions.Nil(err, "unexpected error when creating new resource watcher")
}

func TestResourceWatcher_Start(t *testing.T) {
	var assertions = assert.New(t)
	var dummyStore = store.CurrentStore.(*test.DummyStore)
	defer test.LogRecorder.Reset()

	go watcher.Start()
	time.Sleep(3 * time.Second)

	fmt.Println("Adding subscriptions...")
	processSubscriptions("add")
	time.Sleep(1 * time.Second)
	assertions.Equal(len(subscriptions), dummyStore.AddCalls, "unexpected amount of add calls in the store")

	fmt.Println("Updating subscriptions...")
	processSubscriptions("update")
	time.Sleep(1 * time.Second)
	assertions.Equal(len(subscriptions), dummyStore.UpdateCalls, "unexpected amount of update calls in the store")

	fmt.Println("Deleting subscriptions...")
	processSubscriptions("delete")
	time.Sleep(1 * time.Second)
	assertions.Equal(len(subscriptions), dummyStore.DeleteCalls, "unexpected amount of delete calls in the store")

	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.ErrorLevel, zerolog.PanicLevel), "found unexpected errors and/or panics in the logs")
}

func TestResourceWatcher_Stop(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()
	watcher.Stop()
	time.Sleep(3 * time.Second)
	assertions.Equal(0, test.LogRecorder.GetRecordCount(zerolog.WarnLevel, zerolog.ErrorLevel, zerolog.PanicLevel), "found unexpected warnings, errors and/or panics in the logs")
}
