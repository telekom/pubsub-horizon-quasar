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
```

### Run Application
```bash
# Generate default config file
./quasar init

# Run in provisiosining mode
./quasar run

# Run in watcher mode (watches kubernetes resources)
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
Run linting to check code quality and format/fix:
```bash
golangci-lint run --fix
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

This project follows the Uber Go Style Guide with additional project-specific conventions. See @pubsub-horizon-shareddata/CLAUDE.md

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


---

## Additional Instructions
- Use zerolog instead of fmt
- Whenever you finish work, run the linter and the entire test suite. If issues arise, fix them.
