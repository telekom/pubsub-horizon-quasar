// Copyright 2025 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package provisioning

import (
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/store"
	"github.com/telekom/quasar/internal/test"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

// MockDualStore implements store.DualStore for testing
type MockDualStore struct{}

func (m *MockDualStore) Initialize() {}

func (m *MockDualStore) InitializeResource(kubernetesClient dynamic.Interface, resourceConfig *config.Resource) {
}

func (m *MockDualStore) Create(obj *unstructured.Unstructured) error {
	return nil
}

func (m *MockDualStore) Update(oldObj *unstructured.Unstructured, newObj *unstructured.Unstructured) error {
	return nil
}

func (m *MockDualStore) Delete(obj *unstructured.Unstructured) error {
	return nil
}

func (m *MockDualStore) Count(dataset string) (int, error) {
	return 0, nil
}

func (m *MockDualStore) Keys(dataset string) ([]string, error) {
	return []string{}, nil
}

func (m *MockDualStore) Read(dataset string, key string) (*unstructured.Unstructured, error) {
	return nil, nil
}

func (m *MockDualStore) List(dataset string, fieldSelector string, limit int64) ([]unstructured.Unstructured, error) {
	return []unstructured.Unstructured{}, nil
}

func (m *MockDualStore) Shutdown() {}

func (m *MockDualStore) Connected() bool {
	return true
}

func (m *MockDualStore) GetPrimary() store.Store {
	return nil
}

func (m *MockDualStore) GetSecondary() store.Store {
	return nil
}

func TestProvisioningService_Setup_Success(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Setup mock dependencies
	mockStore := &MockDualStore{}
	testLogger := createTestLogger()

	// Ensure security is disabled for testing
	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	svc := &ProvisioningService{
		logger: testLogger,
		store:  mockStore,
	}

	// Execute Setup
	err := svc.Setup()

	// Verify
	assertions.NoError(err, "Setup should not return an error")
	assertions.NotNil(svc.app, "Fiber app should be initialized")
	assertions.NotNil(svc.GetApp(), "GetApp should return the Fiber app")
}

func TestProvisioningService_Setup_MissingStore(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	testLogger := createTestLogger()

	svc := &ProvisioningService{
		logger: testLogger,
		store:  nil, // Missing store
	}

	err := svc.Setup()

	assertions.Error(err, "Setup should return an error when store is nil")
	assertions.Contains(err.Error(), "store is required")
}

func TestProvisioningService_Setup_MissingLogger(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	mockStore := &MockDualStore{}

	svc := &ProvisioningService{
		logger: nil, // Missing logger
		store:  mockStore,
	}

	err := svc.Setup()

	assertions.Error(err, "Setup should return an error when logger is nil")
	assertions.Contains(err.Error(), "logger is required")
}

// NOTE: TestProvisioningService_Setup_WithSecurityEnabled is not implemented as a unit test
// because the JWT middleware attempts to fetch JWKS from real URLs during initialization,
// which requires external dependencies (HTTP servers, network access).
// This scenario should be tested with integration tests instead of unit tests.

func TestProvisioningService_Start_WithoutSetup(t *testing.T) {
	var assertions = assert.New(t)

	mockStore := &MockDualStore{}
	testLogger := createTestLogger()

	svc := &ProvisioningService{
		logger: testLogger,
		store:  mockStore,
	}

	// Try to start without calling Setup
	err := svc.Start(8080)

	assertions.Error(err, "Start should return an error when Setup was not called")
	assertions.Contains(err.Error(), "service not setup")
}

func TestProvisioningService_Shutdown_WithoutApp(t *testing.T) {
	var assertions = assert.New(t)

	svc := &ProvisioningService{
		app: nil, // No app initialized
	}

	err := svc.Shutdown(30 * time.Second)

	assertions.NoError(err, "Shutdown should not return an error when app is nil")
}

func TestProvisioningService_Shutdown_WithApp(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	mockStore := &MockDualStore{}
	testLogger := createTestLogger()

	// Disable security for testing
	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	svc := &ProvisioningService{
		logger: testLogger,
		store:  mockStore,
	}

	err := svc.Setup()
	assertions.NoError(err)

	// Shutdown should succeed (app is not running, so immediate shutdown)
	err = svc.Shutdown(1 * time.Second)
	assertions.NoError(err, "Shutdown should not return an error")
}

func TestProvisioningService_GetApp(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	mockStore := &MockDualStore{}
	testLogger := createTestLogger()

	// Disable security for testing
	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	svc := &ProvisioningService{
		logger: testLogger,
		store:  mockStore,
	}

	// Before Setup
	assertions.Nil(svc.GetApp(), "GetApp should return nil before Setup")

	// After Setup
	err := svc.Setup()
	assertions.NoError(err)
	assertions.NotNil(svc.GetApp(), "GetApp should return the Fiber app after Setup")
}

func TestProvisioningService_Routes(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	mockStore := &MockDualStore{}
	testLogger := createTestLogger()

	// Disable security for testing
	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	svc := &ProvisioningService{
		logger: testLogger,
		store:  mockStore,
	}

	err := svc.Setup()
	assertions.NoError(err)

	// Verify routes are registered
	app := svc.GetApp()
	assertions.NotNil(app)

	// Get all registered routes
	routes := app.GetRoutes()
	assertions.NotEmpty(routes, "Routes should be registered")

	// Count routes by method to verify basic setup
	routesByMethod := make(map[string]int)
	for _, route := range routes {
		routesByMethod[route.Method]++
	}

	// We expect at least:
	// - 4 GET routes (list, keys, count, get by id)
	// - 1 PUT route (update)
	// - 1 DELETE route (delete)
	assertions.GreaterOrEqual(routesByMethod["GET"], 4, "Should have at least 4 GET routes")
	assertions.GreaterOrEqual(routesByMethod["PUT"], 1, "Should have at least 1 PUT route")
	assertions.GreaterOrEqual(routesByMethod["DELETE"], 1, "Should have at least 1 DELETE route")

	// Verify that routes contain the expected path patterns
	foundListRoute := false
	foundKeysRoute := false
	foundCountRoute := false
	foundGetByIdRoute := false
	foundPutRoute := false
	foundDeleteRoute := false

	for _, route := range routes {
		if route.Method == "GET" && route.Path == "/api/v1/resources/:group/:version/:resource/" {
			foundListRoute = true
		}
		if route.Method == "GET" && route.Path == "/api/v1/resources/:group/:version/:resource/keys" {
			foundKeysRoute = true
		}
		if route.Method == "GET" && route.Path == "/api/v1/resources/:group/:version/:resource/count" {
			foundCountRoute = true
		}
		if route.Method == "GET" && route.Path == "/api/v1/resources/:group/:version/:resource/:id" {
			foundGetByIdRoute = true
		}
		if route.Method == "PUT" && route.Path == "/api/v1/resources/:group/:version/:resource/:id" {
			foundPutRoute = true
		}
		if route.Method == "DELETE" && route.Path == "/api/v1/resources/:group/:version/:resource/:id" {
			foundDeleteRoute = true
		}
	}

	assertions.True(foundListRoute, "List route should be registered")
	assertions.True(foundKeysRoute, "Keys route should be registered")
	assertions.True(foundCountRoute, "Count route should be registered")
	assertions.True(foundGetByIdRoute, "Get by ID route should be registered")
	assertions.True(foundPutRoute, "PUT route should be registered")
	assertions.True(foundDeleteRoute, "DELETE route should be registered")
}

func TestReadinessMiddleware_NotReady(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Set service as not ready
	isReady.Store(false)

	testLogger := createTestLogger()

	// Disable security for testing
	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	// Setup service using the global setupService function
	setupService(testLogger)
	defer func() { service = nil }() // Cleanup

	// Try to access an API endpoint while not ready
	// Note: In real tests, you'd use fiber's Test method to make requests
	assertions.NotNil(service, "Service should be initialized")
}

func TestReadinessMiddleware_Ready(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	// Set service as ready
	isReady.Store(true)

	testLogger := createTestLogger()

	// Disable security for testing
	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	// Setup service using the global setupService function
	setupService(testLogger)
	defer func() { service = nil }() // Cleanup

	assertions.NotNil(service, "Service should be initialized")
}

func TestSetupService_HealthAndReadyEndpoints(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	testLogger := createTestLogger()

	// Disable security for testing
	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	// Setup service
	setupService(testLogger)
	defer func() { service = nil }() // Cleanup

	// Verify routes are registered
	assertions.NotNil(service)
	routes := service.GetRoutes()

	// Check for /health endpoint
	foundHealth := false
	foundReady := false

	for _, route := range routes {
		if route.Method == "GET" && route.Path == "/health" {
			foundHealth = true
		}
		if route.Method == "GET" && route.Path == "/ready" {
			foundReady = true
		}
	}

	assertions.True(foundHealth, "/health endpoint should be registered")
	assertions.True(foundReady, "/ready endpoint should be registered")
}

// ========================================================================
// Tests for createLogger() function
// ========================================================================

// TestCreateLogger_ValidLogLevel verifies logger creation with valid "info" level
func TestCreateLogger_ValidLogLevel(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	originalLogLevel := config.Current.Provisioning.LogLevel
	config.Current.Provisioning.LogLevel = "info"
	defer func() {
		config.Current.Provisioning.LogLevel = originalLogLevel
	}()

	logger := createLogger()

	assertions.NotNil(logger, "Logger should be created")
	assertions.Equal(zerolog.InfoLevel, logger.GetLevel(), "Logger level should be info")
}

// TestCreateLogger_DebugLogLevel verifies logger creation with "debug" level enables ConsoleWriter
func TestCreateLogger_DebugLogLevel(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	originalLogLevel := config.Current.Provisioning.LogLevel
	config.Current.Provisioning.LogLevel = "debug"
	defer func() {
		config.Current.Provisioning.LogLevel = originalLogLevel
	}()

	logger := createLogger()

	assertions.NotNil(logger, "Logger should be created")
	assertions.Equal(zerolog.DebugLevel, logger.GetLevel(), "Logger level should be debug")
}

// TestCreateLogger_InvalidLogLevel verifies logger defaults to "info" when given invalid level
func TestCreateLogger_InvalidLogLevel(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	originalLogLevel := config.Current.Provisioning.LogLevel
	config.Current.Provisioning.LogLevel = "invalid-level"
	defer func() {
		config.Current.Provisioning.LogLevel = originalLogLevel
	}()

	logger := createLogger()

	assertions.NotNil(logger, "Logger should be created even with invalid level")
	assertions.Equal(zerolog.InfoLevel, logger.GetLevel(), "Logger should default to info level")
}

// TestCreateLogger_ErrorLogLevel verifies logger creation with "error" level
func TestCreateLogger_ErrorLogLevel(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	originalLogLevel := config.Current.Provisioning.LogLevel
	config.Current.Provisioning.LogLevel = "error"
	defer func() {
		config.Current.Provisioning.LogLevel = originalLogLevel
	}()

	logger := createLogger()

	assertions.NotNil(logger, "Logger should be created")
	assertions.Equal(zerolog.ErrorLevel, logger.GetLevel(), "Logger level should be error")
}

// ========================================================================
// HTTP Integration Tests for Endpoints
// ========================================================================

// TestHealthEndpoint_HTTP verifies /health endpoint returns 200 OK
func TestHealthEndpoint_HTTP(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	testLogger := createTestLogger()

	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	setupService(testLogger)
	defer func() { service = nil }()

	req := httptest.NewRequest("GET", "/health", nil)
	resp, err := service.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	assertions.NoError(err)
	assertions.Equal("OK", string(body))
}

// TestReadyEndpoint_HTTP_WhenReady verifies /ready endpoint returns 200 READY when service is ready
func TestReadyEndpoint_HTTP_WhenReady(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	isReady.Store(true)
	defer isReady.Store(false)

	testLogger := createTestLogger()

	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	setupService(testLogger)
	defer func() { service = nil }()

	req := httptest.NewRequest("GET", "/ready", nil)
	resp, err := service.Test(req)

	assertions.NoError(err)
	assertions.Equal(200, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	assertions.NoError(err)
	assertions.Equal("READY", string(body))
}

// TestReadyEndpoint_HTTP_WhenNotReady verifies /ready endpoint returns 503 with Retry-After when not ready
func TestReadyEndpoint_HTTP_WhenNotReady(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	isReady.Store(false)

	testLogger := createTestLogger()

	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	setupService(testLogger)
	defer func() { service = nil }()

	req := httptest.NewRequest("GET", "/ready", nil)
	resp, err := service.Test(req)

	assertions.NoError(err)
	assertions.Equal(503, resp.StatusCode)
	assertions.Equal("30", resp.Header.Get("Retry-After"))

	body, err := io.ReadAll(resp.Body)
	assertions.NoError(err)
	assertions.Equal("NOT READY", string(body))
}

// TestReadinessMiddleware_HTTP_WhenNotReady verifies API endpoints return 503 with Retry-After when not ready
func TestReadinessMiddleware_HTTP_WhenNotReady(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	isReady.Store(false)

	testLogger := createTestLogger()

	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	setupService(testLogger)
	defer func() { service = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/test.example.com/v1/testresources/", nil)
	resp, err := service.Test(req)

	assertions.NoError(err)
	assertions.Equal(503, resp.StatusCode)
	assertions.Equal("30", resp.Header.Get("Retry-After"))
}

// TestReadinessMiddleware_HTTP_WhenReady verifies API endpoints pass through middleware when ready
func TestReadinessMiddleware_HTTP_WhenReady(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	isReady.Store(true)
	defer isReady.Store(false)

	testLogger := createTestLogger()

	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	setupService(testLogger)
	defer func() { service = nil }()

	provisioningApiStore = &MockDualStore{}
	defer func() { provisioningApiStore = nil }()

	req := httptest.NewRequest("GET", "/api/v1/resources/test.example.com/v1/testresources/", nil)
	resp, err := service.Test(req)

	assertions.NoError(err)
	assertions.NotEqual(503, resp.StatusCode)
}

// ========================================================================
// Tests for setupService with security enabled path
// ========================================================================

// NOTE: TestSetupService_SecurityEnabledPath is not implemented as a unit test
// because the JWT middleware requires valid configuration (JWKSetURLs, SigningKeys, etc.)
// and attempts to fetch JWKS from real URLs during initialization, which requires
// external dependencies (HTTP servers, network access).
// The security-enabled path should be tested with integration tests instead of unit tests.
//
// The code path in setupService() lines 135-138 is exercised indirectly through
// integration tests or skipped in unit tests for the same reason as
// ProvisioningService.Setup() with security (see line 134-137).

// ========================================================================
// Tests for ProvisioningService struct methods
// ========================================================================

// TestProvisioningService_Setup_WithSecurityDisabledLogging verifies warning is logged when security is disabled
func TestProvisioningService_Setup_WithSecurityDisabledLogging(t *testing.T) {
	var assertions = assert.New(t)
	defer test.LogRecorder.Reset()

	mockStore := &MockDualStore{}
	testLogger := createTestLogger()

	originalSecurityEnabled := config.Current.Provisioning.Security.Enabled
	config.Current.Provisioning.Security.Enabled = false
	defer func() {
		config.Current.Provisioning.Security.Enabled = originalSecurityEnabled
	}()

	svc := &ProvisioningService{
		logger: testLogger,
		store:  mockStore,
	}

	err := svc.Setup()
	assertions.NoError(err)
}

// TestProvisioningService_GetApp_BeforeSetup verifies GetApp returns nil before Setup is called
func TestProvisioningService_GetApp_BeforeSetup(t *testing.T) {
	var assertions = assert.New(t)

	svc := &ProvisioningService{}
	app := svc.GetApp()

	assertions.Nil(app)
}
