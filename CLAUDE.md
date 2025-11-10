<!--
Copyright 2025 Deutsche Telekom AG

SPDX-License-Identifier: Apache-2.0
-->
# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Quasar is a Kubernetes configuration controller that synchronizes caches (Hazelcast) and databases (MongoDB) with either the state of Custom Resources (CRs) or using the provisioning API. It is part of the Horizon ecosystem. The service operates in two distinct modes:

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
go test -v ./... -coverprofile=./cover.out -covermode=atomic -coverpkg=./... -tags=testing -p=1

# Run tests in a specific package
go test -v ./internal/store -tags=testing -p=1

# Run a specific test
go test -v ./internal/store -run TestHazelcastStore_Create -tags=testing -p=1
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
Run linting to check code quality:
```bash
golangci-lint run .
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
- On startup, performs synchronous MongoDB→Hazelcast cache population before initializing the HTTP server. No endpoints are available during this time.
- Liveness probe endpoint (`/livez`) becomes available after initial sync completes
- Readiness probe endpoint (`/readyz`) becomes available after initial sync completes

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

## Code Conventions

This project follows the Uber Go Style Guide with additional project-specific conventions. Key principles include:

### Naming Conventions
- **Meaningful names** for all variables, functions, structs
- **Error variables**: Named after the function that returns them, e.g., `errValidateCustomerFeedback := validateCustomerFeedback(...)`
- **Error types**: Prefix with `Err` or `err` for exported/unexported error variables; suffix with `Error` for custom error types
- **Type aliases**: Wrap external types in package-local type aliases for easier import management
- **Package names**: Short, concise, evocative, lowercase, single-word names (avoid "common", "util", "shared", "lib" as catch-all packages)
- **Interface names**: One-method interfaces use method name + "-er" suffix (Reader, Writer, Formatter)
- **MixedCaps**: Use MixedCaps or mixedCaps rather than underscores for multiword names
- **Functions/Variables**: Exported start with uppercase, unexported with lowercase (camel case)
- **Constants**: Use all capital letters with underscores, e.g., `MAX_RETRY_COUNT`
- **Boolean variables**: Prefix with Has, Is, Can, or Allow, e.g., `isConnected`, `hasPermission`
- **Getters**: Avoid "Get" prefix; use `user.Name()` instead of `user.GetName()`
- **File names**: Single lowercase words; compound names use underscores; test files use `_test.go` suffix
- **Unexported globals**: Prefix with `_` (except error values with `err` prefix)

### Pointers and Interfaces
- **Never use pointers to interfaces** - interfaces are already reference types
- **Verify interface compliance at compile time**: Use `var _ Interface = (*Type)(nil)` for exported types
- **Receivers**: Methods with value receivers can be called on pointers and values; pointer receivers only on pointers/addressable values
- **Accept interfaces, return structs**: Interfaces declared on consumer side, not producer side

### Error Handling
- **Always check and handle errors explicitly**
- **Handle errors once**: Don't log and return; choose one approach
- **Error wrapping**: Use `fmt.Errorf("context: %w", err)` to wrap errors for traceability
- **Error matching**: Use `%w` if callers should match the error; use `%v` to obfuscate
- **Avoid "failed to" prefix**: Keep context succinct (use "new store" not "failed to create new store")
- **Return errors from functions**: Only call `os.Exit` or `log.Fatal` in `main()`

### Nil Handling and Zero Values
- **Internal functions**: Do NOT check input parameters for nil (caller's responsibility)
- **External functions**: DO check input parameters for nil
- **Exception**: Always validate input structs
- **nil is a valid slice**: Return `nil` instead of `[]T{}` for empty slices; check `len(s) == 0` not `s == nil`
- **Zero-value mutexes are valid**: Don't use `new(sync.Mutex)`; use `var mu sync.Mutex`

### Structs
- **Constructors required**: Always use constructors to instantiate structs (except parameter structs)
- **Field names in initialization**: Always specify field names when initializing structs (enforced by `go vet`)
- **Omit zero values**: Don't specify zero-value fields unless they provide meaningful context
- **Use `var` for zero-value structs**: `var user User` instead of `user := User{}`
- **Struct references**: Use `&T{}` instead of `new(T)` for consistency
- **Parameter structs**: Instantiate inline, must be validated in the function
- **Avoid embedding in public structs**: Embedding leaks implementation details and inhibits evolution
- **Embedded fields**: Place at top of struct with blank line separator

### Immutability and State
- **Favor immutability**: Pass structs by value and return new structs rather than mutating pointers (unless performance-critical)
- **Data flow transparency**: Write code with straightforward, transparent data flow
- **Avoid mutable globals**: Use dependency injection instead of global variables
- **Avoid `init()`**: If unavoidable, Be deterministic, avoid I/O, no global state; use constructors or main() instead

### Dependencies and Concurrency
- Use **dependency injection** (constructor functions)
- Avoid global state
- Follow **inversion of control** principle
- **Don't panic**: Return errors instead of panicking (except for truly irrecoverable situations)
- **Use goroutines safely**: Always have a way to stop goroutines and wait for them to exit
- **No goroutines in `init()`**: Expose objects that manage goroutine lifetimes
- **Propagate context**: Always propagate `context.Context` for cancellation
- **Defer to clean up**: Use defer for resource cleanup (files, locks, etc.)

### Maps, Slices, and Collections
- **Copy slices and maps at boundaries**: Prevent unintended mutations when receiving/returning
- **Specify container capacity**: Use `make(map[T]T, size)` and `make([]T, 0, capacity)` when size is known
- **Channel size**: Channels should be unbuffered or size 1 (any other size requires scrutiny)
- **Map initialization**: Use `make()` for empty maps; use literals for fixed sets of elements

### Time Handling
- **Use `time.Time`** for instants of time (not int/string)
- **Use `time.Duration`** for periods of time (not int/float)
- **Use RFC 3339** format for string timestamps when needed
- **AddDate vs Add**: Use `AddDate` for calendar arithmetic; use `Add` for duration arithmetic

### Variables and Scope
- **Short variable declarations**: Use `:=` when setting explicit values; use `var` for zero values
- **Reduce scope**: Declare variables in smallest scope possible; use inline declarations with if statements
- **Local constants**: Don't make constants global unless used across functions/files
- **Top-level declarations**: Use `var` keyword without type (unless type differs from expression)

### Code Organization and Style
- **Use guard clauses (reverse ifs)**: Check for error/invalid conditions first and return early to avoid deeply nested code
- **Reduce nesting**: Handle errors/special cases first and return early
- **Unnecessary else**: Eliminate else blocks when variable can be set with single if
- **Reduce nesting**: Handle errors/special cases first and return early
- **Unnecessary else**: Eliminate else blocks when variable can be set with single if
- **Function grouping**: Sort functions by receiver; place utility functions at end
- **Import groups**: Standard library, then everything else (blank line between)
- **Group similar declarations**: Use `const ()`, `var ()`, `type ()` blocks for related declarations
- **Be consistent**: Consistency is more important than individual preferences

### Testing
- **Use table-driven tests** with subtests for repetitive test logic
- **Avoid unnecessary complexity**: Split complex table tests into multiple tests or tables
- **Test tables convention**: Slice named `tests`, variable `tt`, fields prefixed with `give`/`want`
- **Mock external interfaces**: Use Mockery for generating mocks
- **Define mock expectations**: In mutation functions within test tables
- **Separate test types**: Unit tests (fast) vs integration tests (slower, use `dockertest`)
- **Coverage**: Ensure test coverage for all exported functions
- **Use goroutine leak detection**: Use `go.uber.org/goleak` to test for goroutine leaks, if needed

### Performance
- **Prefer `strconv` over `fmt`** for primitive conversions
- **Avoid repeated string-to-byte conversions**: Convert once and reuse
- **Atomic operations**: If needed, use `sync/atomic` or `go.uber.org/atomic` package for thread-safe operations

### Patterns
- **Functional Options**: Use for optional constructor arguments (variadic `...Option`)
- **Exit Once**: Prefer single `os.Exit` call in `main()`; use `run() error` pattern
- **Field tags**: Always use field tags in marshaled structs (json, yaml, etc.)

### Type Safety
- **Handle type assertion failures**: Always use "comma ok" idiom: `t, ok := i.(string)`
- **Start enums at one**: Use `iota + 1` so zero value is invalid/unknown
- **Avoid built-in names**: Don't shadow built-in identifiers (error, string, etc.)
- **Use raw string literals**: Use backticks for strings with quotes/backslashes

### Linting
- Run `goimports` on save
- Run `golint` and `go vet` to check for errors
- Use `golangci-lint` as primary linter with recommended configuration
- Required linters: errcheck, goimports, golint, govet, staticcheck

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
