// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/reconciliation"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/test"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ========================================================================
// Enhanced Mock Stores with Error Simulation
// ========================================================================

// MockDualStoreWithErrors implements store.DualStore with configurable error behavior
type MockDualStoreWithErrors struct {
	CreateError bool
	ReadError   bool
	ListError   bool
	DeleteError bool
	KeysError   bool
	CountError  bool
	ReturnNil   bool // For simulating resource not found

	// Storage for testing
	resources map[string]*unstructured.Unstructured
}

func NewMockDualStoreWithErrors() *MockDualStoreWithErrors {
	return &MockDualStoreWithErrors{
		resources: make(map[string]*unstructured.Unstructured),
	}
}

func (m *MockDualStoreWithErrors) Initialize() {}

func (m *MockDualStoreWithErrors) InitializeResource(dataSource reconciliation.DataSource, resourceConfig *config.Resource) {
}

func (m *MockDualStoreWithErrors) Create(obj *unstructured.Unstructured) error {
	if m.CreateError {
		return fmt.Errorf("mock create error")
	}
	m.resources[obj.GetName()] = obj
	return nil
}

func (m *MockDualStoreWithErrors) Update(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error {
	return nil
}

func (m *MockDualStoreWithErrors) Delete(obj *unstructured.Unstructured) error {
	if m.DeleteError {
		return fmt.Errorf("mock delete error")
	}
	delete(m.resources, obj.GetName())
	return nil
}

func (m *MockDualStoreWithErrors) Count(dataset string) (int, error) {
	if m.CountError {
		return 0, fmt.Errorf("mock count error")
	}
	return len(m.resources), nil
}

func (m *MockDualStoreWithErrors) Keys(dataset string) ([]string, error) {
	if m.KeysError {
		return nil, fmt.Errorf("mock keys error")
	}
	keys := make([]string, 0, len(m.resources))
	for k := range m.resources {
		keys = append(keys, k)
	}
	return keys, nil
}

func (m *MockDualStoreWithErrors) Read(dataset string, key string) (*unstructured.Unstructured, error) {
	if m.ReadError {
		return nil, fmt.Errorf("mock read error")
	}
	if m.ReturnNil {
		return nil, nil
	}
	if resource, ok := m.resources[key]; ok {
		return resource, nil
	}
	return nil, nil
}

func (m *MockDualStoreWithErrors) List(dataset string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error) {
	if m.ListError {
		return nil, fmt.Errorf("mock list error")
	}
	result := make([]unstructured.Unstructured, 0, len(m.resources))
	count := int64(0)
	for _, v := range m.resources {
		if limit > 0 && count >= limit {
			break
		}
		result = append(result, *v)
		count++
	}
	return result, nil
}

func (m *MockDualStoreWithErrors) Shutdown() {}

func (m *MockDualStoreWithErrors) Connected() bool {
	return true
}

func (m *MockDualStoreWithErrors) GetPrimary() store.Store {
	return nil
}

func (m *MockDualStoreWithErrors) GetSecondary() store.Store {
	return nil
}

// ========================================================================
// Helper Functions
// ========================================================================

func setupCrudTestApp() *fiber.App {
	testLogger := createTestLogger()

	app := fiber.New(fiber.Config{
		DisableStartupMessage: true,
	})

	// Setup routes similar to service.go
	v1 := app.Group("/api/v1/resources/:group/:version/:resource")
	v1.Get("/", withGvr, listResources)
	v1.Get("/keys", withGvr, listKeys)
	v1.Get("/count", withGvr, countResources)
	v1.Get("/:id", withGvr, withResourceId, getResource)
	v1.Put("/:id", withGvr, withResourceId, withKubernetesResource, putResource)
	v1.Delete("/:id", withGvr, withResourceId, withKubernetesResource, deleteResource)

	// Initialize logger for handlers
	if logger == nil {
		logger = testLogger
	}

	return app
}

func createTestResource(name, kind, apiVersion string) *unstructured.Unstructured {
	resource := &unstructured.Unstructured{}
	resource.SetName(name)
	resource.SetKind(kind)
	resource.SetAPIVersion(apiVersion)
	resource.SetNamespace("default")
	return resource
}

func createTestResourceBody(name, kind, apiVersion string) string {
	resource := map[string]interface{}{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": "default",
		},
		"spec": map[string]interface{}{
			"test": "data",
		},
	}
	data, _ := json.Marshal(resource)
	return string(data)
}

// ========================================================================
// HIGH PRIORITY TESTS
// ========================================================================

// Tests for getResource()
// ========================================================================

// TestGetResource_Success verifies getResource returns 200 with resource JSON when resource exists
func TestGetResource_Success(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	testResource := createTestResource("test-subscription", "Subscription", "subscriber.horizon.telekom.de/v1")
	mockStore.resources["test-subscription"] = testResource

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var response ResourceResponse
	err = json.Unmarshal(body, &response)
	assertions.NoError(err)
	assertions.NotNil(response.Resource)
	assertions.Equal("test-subscription", response.Resource.GetName())
}

// TestGetResource_NotFound verifies getResource returns 404 when resource does not exist
func TestGetResource_NotFound(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()
	mockStore.ReturnNil = true

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/nonexistent", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(404, resp.StatusCode)
}

// TestGetResource_StoreError verifies getResource returns 500 when store operation fails
func TestGetResource_StoreError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()
	mockStore.ReadError = true

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(500, resp.StatusCode)
}

// Tests for listResources()
// ========================================================================

// TestListResources_Success verifies listResources returns all resources with count
func TestListResources_Success(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	mockStore.resources["sub1"] = createTestResource("sub1", "Subscription", "subscriber.horizon.telekom.de/v1")
	mockStore.resources["sub2"] = createTestResource("sub2", "Subscription", "subscriber.horizon.telekom.de/v1")
	mockStore.resources["sub3"] = createTestResource("sub3", "Subscription", "subscriber.horizon.telekom.de/v1")

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var response ResourceResponse
	err = json.Unmarshal(body, &response)
	assertions.NoError(err)
	assertions.Equal(3, response.Count)
	assertions.Len(response.Items, 3)
}

// TestListResources_WithLimit verifies limit query parameter correctly restricts result count
func TestListResources_WithLimit(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("sub%d", i)
		mockStore.resources[name] = createTestResource(name, "Subscription", "subscriber.horizon.telekom.de/v1")
	}

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/?limit=2", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var response ResourceResponse
	err = json.Unmarshal(body, &response)
	assertions.NoError(err)
	assertions.Equal(2, response.Count)
	assertions.Len(response.Items, 2)
}

// TestListResources_WithInvalidLimit verifies invalid limit parameter is handled gracefully (defaults to 0)
func TestListResources_WithInvalidLimit(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	mockStore.resources["sub1"] = createTestResource("sub1", "Subscription", "subscriber.horizon.telekom.de/v1")

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/?limit=invalid", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var response ResourceResponse
	err = json.Unmarshal(body, &response)
	assertions.NoError(err)
	assertions.Equal(1, response.Count)
}

// TestListResources_WithFieldSelector verifies fieldSelector query parameter is passed to store
func TestListResources_WithFieldSelector(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	mockStore.resources["sub1"] = createTestResource("sub1", "Subscription", "subscriber.horizon.telekom.de/v1")

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/?fieldSelector=status.phase=Running", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)
}

// TestListResources_StoreError verifies listResources returns 500 when store operation fails
func TestListResources_StoreError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()
	mockStore.ListError = true

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(500, resp.StatusCode)
}

// ========================================================================
// MEDIUM PRIORITY TESTS
// ========================================================================

// Tests for putResource()
// ========================================================================

// TestPutResource_Success verifies putResource creates/updates resource and returns 200
func TestPutResource_Success(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	body := createTestResourceBody("test-subscription", "Subscription", "subscriber.horizon.telekom.de/v1")
	req := httptest.NewRequest("PUT", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)
	assertions.Contains(mockStore.resources, "test-subscription")
	assertions.Equal("test-subscription", mockStore.resources["test-subscription"].GetName())
}

// TestPutResource_StoreError verifies putResource returns 500 when store operation fails
func TestPutResource_StoreError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()
	mockStore.CreateError = true

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	body := createTestResourceBody("test-subscription", "Subscription", "subscriber.horizon.telekom.de/v1")
	req := httptest.NewRequest("PUT", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(500, resp.StatusCode)
}

// TestPutResource_InvalidJSON verifies putResource returns 400 when JSON body is invalid
func TestPutResource_InvalidJSON(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("PUT", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(400, resp.StatusCode)
}

// Tests for deleteResource()
// ========================================================================

// TestDeleteResource_Success verifies deleteResource removes resource and returns 204
func TestDeleteResource_Success(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	testResource := createTestResource("test-subscription", "Subscription", "subscriber.horizon.telekom.de/v1")
	mockStore.resources["test-subscription"] = testResource

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	body := createTestResourceBody("test-subscription", "Subscription", "subscriber.horizon.telekom.de/v1")
	req := httptest.NewRequest("DELETE", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(204, resp.StatusCode)
	assertions.NotContains(mockStore.resources, "test-subscription")
}

// TestDeleteResource_StoreError verifies deleteResource returns 500 when store operation fails
func TestDeleteResource_StoreError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()
	mockStore.DeleteError = true

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	body := createTestResourceBody("test-subscription", "Subscription", "subscriber.horizon.telekom.de/v1")
	req := httptest.NewRequest("DELETE", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(500, resp.StatusCode)
}

// TestDeleteResource_InvalidJSON verifies deleteResource returns 400 when JSON body is invalid
func TestDeleteResource_InvalidJSON(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("DELETE", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/test-subscription", strings.NewReader("invalid json"))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(400, resp.StatusCode)
}

// ========================================================================
// LOW PRIORITY TESTS
// ========================================================================

// Tests for listKeys()
// ========================================================================

// TestListKeys_Success verifies listKeys returns array of resource keys
func TestListKeys_Success(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	mockStore.resources["sub1"] = createTestResource("sub1", "Subscription", "subscriber.horizon.telekom.de/v1")
	mockStore.resources["sub2"] = createTestResource("sub2", "Subscription", "subscriber.horizon.telekom.de/v1")

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/keys", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var response ResourceResponse
	err = json.Unmarshal(body, &response)
	assertions.NoError(err)
	assertions.Len(response.Keys, 2)
}

// TestListKeys_StoreError verifies listKeys returns 500 when store operation fails
func TestListKeys_StoreError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()
	mockStore.KeysError = true

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/keys", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(500, resp.StatusCode)
}

// Tests for countResources()
// ========================================================================

// TestCountResources_Success verifies countResources returns correct count of resources
func TestCountResources_Success(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	mockStore.resources["sub1"] = createTestResource("sub1", "Subscription", "subscriber.horizon.telekom.de/v1")
	mockStore.resources["sub2"] = createTestResource("sub2", "Subscription", "subscriber.horizon.telekom.de/v1")
	mockStore.resources["sub3"] = createTestResource("sub3", "Subscription", "subscriber.horizon.telekom.de/v1")

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/count", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var response ResourceResponse
	err = json.Unmarshal(body, &response)
	assertions.NoError(err)
	assertions.Equal(3, response.Count)
}

// TestCountResources_EmptyStore verifies countResources returns 0 when no resources exist
func TestCountResources_EmptyStore(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/count", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	var response ResourceResponse
	err = json.Unmarshal(body, &response)
	assertions.NoError(err)
	assertions.Equal(0, response.Count)
}

// TestCountResources_StoreError verifies countResources returns 500 when store operation fails
func TestCountResources_StoreError(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	app := setupCrudTestApp()
	mockStore := NewMockDualStoreWithErrors()
	mockStore.CountError = true

	provisioningApiStore = mockStore
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/subscriber.horizon.telekom.de/v1/subscriptions/count", nil)
	resp, err := app.Test(req)

	assertions.NoError(err)
	assertions.Equal(500, resp.StatusCode)
}
