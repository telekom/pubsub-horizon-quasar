# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Quasar is a Kubernetes configuration controller that synchronizes the state of Custom Resources (CRs) with caches (Hazelcast) and databases (MongoDB). It is part of the Horizon ecosystem. The service operates in two distinct modes:

1. **Watcher Mode**: Watches Kubernetes custom resources using informers and synchronizes changes to configured stores (Hazelcast, MongoDB, Redis)
2. **Provisioning Mode**: Exposes an HTTP REST API (using Fiber) for provisioning/CRUD operations on resources stored in the configured stores

## Commands

### Build
```bash
go build
```

### Run Tests
```bash
# Run all tests with coverage
go test -v ./... -coverprofile=./cover.out -covermode=atomic -coverpkg=./... -tags=testing

# Run tests in a specific package
go test -v ./internal/store -tags=testing

# Run a specific test
go test -v ./internal/store -run TestHazelcastStore_Create -tags=testing
```

### Run Application
```bash
# Generate default config file
./quasar init

# Run in watcher mode (watches Kubernetes resources)
./quasar run

# Run with custom kubeconfig
./quasar run --kubeconfig /path/to/kubeconfig
```

### Docker Build
```bash
docker build -t horizon-quasar:latest .
```

### Code Quality
The project uses pre-commit hooks:
- REUSE compliance checks (copyright/licensing)
- Conventional commits enforcement

### Linting
Use standard Go tooling:
```bash
go fmt ./...
goimports -w .
golangci-lint run
```

## Architecture

### Operating Modes

**Watcher Mode** (`internal/k8s/watcher.go`):
- Creates Kubernetes informers for configured custom resources
- Watches for Add/Update/Delete events on CRs
- Synchronizes changes to primary and secondary stores via `DualStoreManager`
- Implements fallback mechanism: if Kubernetes API is unavailable on startup, replays resources from MongoDB
- Collects Prometheus metrics for resource counts

**Provisioning Mode** (`internal/provisioning/service.go`):
- Exposes REST API using Fiber framework (port 8081 by default)
- Provides CRUD operations on resources stored in the configured stores
- Implements JWT-based authentication with trusted issuers and clients (can be disabled)
- On startup, performs synchronous MongoDB→Hazelcast cache population before accepting API requests
- Readiness probes (`/ready`) return 503 until initial sync completes
- Health endpoint (`/health`) available immediately

### Core Components

**Store Abstraction** (`internal/store/`):
- `Store` interface: Unified abstraction for Hazelcast, MongoDB, and Redis
- `DualStoreManager`: Manages primary and secondary stores, writes synchronously to primary, asynchronously to secondary
- Primary store: source of truth for read operations
- Secondary store: optional backup/fallback store
- In watcher mode: Hazelcast (primary), MongoDB (secondary)
- In provisioning mode: MongoDB (primary), Hazelcast (secondary)

**Configuration** (`internal/config/`):
- Uses Viper for configuration loading (environment variables + config file)
- `Resource`: Defines which Kubernetes CRs to watch, Prometheus labels, indexes for Mongo/Hazelcast
- `Mode` type: `provisioning` or `watcher`
- Dual store configuration per mode

**Reconciliation** (`internal/reconciliation/`):
- Periodic reconciliation to detect drift between Kubernetes state and store state
- Two modes: `full` (re-sync all resources) or `incremental` (detect and sync only missing entries)
- Uses mutex to prevent concurrent reconciliations
- Only runs when store is connected

**Fallback** (`internal/fallback/`):
- MongoDB-based fallback mechanism
- When Kubernetes API is unavailable at startup, replays resources from MongoDB into cache
- Only applicable in watcher mode

**Metrics** (`internal/metrics/`):
- Prometheus metrics for resource counts
- Custom labels per resource configuration
- Separate metrics server (port 8080 by default)

**Utils** (`internal/utils/`):
- Graceful shutdown with registered hooks
- Priority-based shutdown (lower priority shuts down first)
- Helper functions for Hazelcast, MongoDB connections
- `AddMissingEnvironment`: Ensures resources have an "environment" field in spec

### Data Flow

**Watcher Mode**:
1. Informer detects CR event (Add/Update/Delete) →
2. Event handler in `watcher.go` →
3. `DualStoreManager` writes to primary (Hazelcast) synchronously, secondary (MongoDB) asynchronously →
4. Prometheus metrics updated

**Provisioning Mode**:
1. HTTP request to `/api/v1/resources/:group/:version/:resource/:id` →
2. Middleware validates JWT (if enabled), checks readiness →
3. Handler calls `DualStoreManager` →
4. Primary store (MongoDB) written synchronously, secondary store (Hazelcast) asynchronously →
5. Response returned

### Testing Patterns

- Uses **table-driven tests** (see convention guidelines)
- Mocks generated with **Mockery**
- Test files include `//go:build testing` tag
- Integration tests use `dockertest` for spinning up MongoDB/Hazelcast containers
- Mock expectations defined in `mockExpectations` mutation functions within test tables

## Code Conventions (from golang-conventions.md)

### Naming
- **Meaningful names** for all variables, functions, structs (no single-letter names except in tight loops)
- **Error variables**: Named after the function that returns them, e.g., `errValidateCustomerFeedback := validateCustomerFeedback(...)`
- **Type aliases**: Wrap external types in package-local type aliases for easier import management
- **Package names**: should be short, concise, evocative. lower case, single-word names
- **Interface names**: one-method interfaces are named by the method name plus an -er suffix or similar modification to construct an agent noun: Reader, Writer, Formatter, CloseNotifier etc.
- **MixedCaps**: use MixedCaps or mixedCaps rather than underscores to write multiword names
- **Functions/Variables**: Exported start with uppercase, unexported with lowercase (camel case)
- **Constants**: Use all capital letters with underscores, e.g., `MAX_RETRY_COUNT`
- **Boolean variables**: Prefix with Has, Is, Can, or Allow, e.g., `isConnected`, `hasPermission`
- **Getters**: Avoid "Get" prefix; use `user.Name()` instead of `user.GetName()`
- **File names**: single lowercase words; compound names use underscores; test files use `_test.go` suffix

### Nil Handling
- **Internal functions**: Do NOT check input parameters for nil (caller's responsibility)
- **External functions**: DO check input parameters for nil
- **Exception**: Always validate input structs (see below)

### Structs
- **Constructors required**: Always use constructors to instantiate structs (except parameter structs)
- **Parameter structs**: Instantiate inline, must be validated in the function
- **Validation**: Parameter structs wrapped in a struct must be validated for expected values

### Interfaces
- **Accept interfaces, return structs**: Interfaces declared on consumer side, not producer side
- Exception: External functions may declare interfaces for consumer convenience

### Immutability
- **Favor immutability**: Pass structs by value and return new structs rather than mutating pointers (unless performance-critical)
- **Data flow transparency**: Write code with straightforward, transparent data flow

### Dependencies
- Use **dependency injection** (constructor functions)
- Avoid global state
- Follow **inversion of control** principle

### Error Handling
- Always check and handle errors explicitly
- Use wrapped errors for traceability: `fmt.Errorf("context: %w", err)`

### Concurrency
- Use goroutines safely with channels or sync primitives
- Always propagate `context.Context` for cancellation
- Defer closing resources

### Testing
- Use **table-driven tests**
- Mock external interfaces (use Mockery)
- Define mock logic in mutation functions within test tables
- Separate unit tests (fast) from integration tests (slower, use `dockertest`)
- Ensure test coverage for all exported functions

## Important Implementation Notes

### Resource Configuration
Each resource in `config.yaml` defines:
- `kubernetes`: GVK (Group/Version/Kind) and namespace to watch
- `prometheus`: Metric labels (fixed values or dynamic from CR fields using `$spec.field` syntax)
- `mongoIndexes`: Indexes to create in MongoDB
- `hazelcastIndexes`: Indexes to create in Hazelcast with name, fields, and type (sorted/hash)

### Store Implementations
- **HazelcastStore**: Uses Hazelcast Go client, maps per resource, supports indexes, periodic reconciliation
- **MongoStore**: Uses MongoDB driver, collections per resource, supports field selectors
- **RedisStore**: Partially implemented, not fully supported

### Shutdown Behavior
- Graceful shutdown via `utils.GracefulShutdown()`
- Registered hooks executed in priority order
- Priority 0: Watchers stop first
- Priority 1: Stores shut down second

### Provisioning API Endpoints
- `GET /api/v1/resources/:group/:version/:resource` - List resources
- `GET /api/v1/resources/:group/:version/:resource/keys` - List resource keys
- `GET /api/v1/resources/:group/:version/:resource/count` - Count resources
- `GET /api/v1/resources/:group/:version/:resource/:id` - Get specific resource
- `PUT /api/v1/resources/:group/:version/:resource/:id` - Create/update resource
- `DELETE /api/v1/resources/:group/:version/:resource/:id` - Delete resource

### Security
- JWT-based authentication with JWK Set URLs
- Trusted client IDs validation
- Can be disabled via `provisioning.security.enabled=false` (not recommended for production)

## Key Files

- `main.go`: Entry point, initializes config and executes CLI
- `internal/cmd/run.go`: Main run command logic, mode switching
- `internal/config/loader.go`: Viper-based configuration loading
- `internal/store/storemanager.go`: DualStoreManager implementation
- `internal/k8s/watcher.go`: Kubernetes informer setup and event handlers
- `internal/provisioning/service.go`: Fiber HTTP service setup and startup logic
- `internal/reconciliation/reconciliation.go`: Periodic reconciliation logic
