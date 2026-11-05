<!--
Copyright 2024 Deutsche Telekom AG

SPDX-License-Identifier: Apache-2.0
-->

<p align="center">
    <img src="docs/img/quasar-logo.png" alt="Quasar logo" width="200">
    <h1 align="center">Quasar</h1>
</p>

<p align="center">
Quasar is a tiny service for synchronizing the state of custom resources with caches or databases.
</p>

<p align="center">
  <a href="#prerequisites">Prerequisites</a> •
  <a href="#building-quasar">Building Quasar</a> •
  <a href="#configuration">Configuration</a> •
  <a href="#running-quasar">Running Quasar</a>
</p>

[![REUSE status](https://api.reuse.software/badge/github.com/telekom/pubsub-horizon-quasar)](https://api.reuse.software/info/github.com/telekom/pubsub-horizon-quasar)
[![Go Test](https://github.com/telekom/pubsub-horizon-quasar/actions/workflows/go-test.yml/badge.svg)](https://github.com/telekom/pubsub-horizon-quasar/actions/workflows/go-test.yml)
[![Ask DeepWiki](https://deepwiki.com/badge.svg)](https://deepwiki.com/telekom/pubsub-horizon-quasar)

## Overview
Quasar is the config-controller powering the Horizon ecosystem. 

> **Note:** Quasar is an essential part of the Horizon ecosystem. Please refer to [documentation of the entire system](https://github.com/telekom/pubsub-horizon) to get the full picture.

## Prerequisites
- A running [Kubernetes](https://github.com/kubernetes/kubernetes) cluster
- A running instance of [MongoDB](https://www.mongodb.com/) or [Redis (_not fully supported yet_)](https://redis.io)

## Building Quasar
### Go build

Install [Go](https://go.dev/) 1.26 or newer, then build the executable:
```bash
go build
```

> Alternatively, you can also follow the Docker build in the following section if you want to build a Docker image without the need to have Golang installed locally.

### Docker build
This repository provides a multi-stage Dockerfile that will also take care of compiling the software, as well as dockerizing Quasar. Simply run:

```bash
docker build -t horizon-quasar:latest  .
```

### Linting
Run linting to check code quality and format/fix:
```bash
golangci-lint run --fix
```

## Configuration
Quasar can be configured using environment variables and/or a configuration file. The following table lists all available configuration options:

**Environment variables aren't officially supported for the `resources` option. It is recommended to configure the `resources` using a configuration/configmap.**

> **Please note:** Redis isn't fully supported (yet) as Hazelcast is the primary cache method of Horizon in its current state.

| Path                                                    | Variable                                                 | Type          | Default                                                                                | Description                                                                                                        |
|---------------------------------------------------------|----------------------------------------------------------|---------------|----------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------|
| logLevel                                                | QUASAR_LOGLEVEL                                          | string        | info                                                                                   | The log-level.                                                                                                     |
| mode                                                    | QUASAR_MODE                                              | string        | provisioning                                                                           | Mode in which Quasar should run (provisioning (api) or watcher (k8s))                                              |
| fallback.type                                           | QUASAR_FALLBACK_TYPE                                     | string        | mongo                                                                                  | The fallback type that should be used. (mongo or none)                                                             |
| fallback.mongo.database                                 | QUASAR_FALLBACK_MONGO_DATABASE                           | string        | horizon                                                                                | The database that should be used to restore the cache in case of a CR unavailability                               |
| fallback.mongo.uri                                      | QUASAR_FALLBACK_MONGO_URI                                | string        | mongodb://localhost:27017                                                              | MongoDB uri of the fallback database.                                                                              |
| reSyncPeriod                                            | QUASAR_RESYNCPERIOD                                      | string        | 30s                                                                                    | Resync-period of the Kubernetes informer.                                                                          |
| store.hazelcast.addresses                               | QUASAR_STORE_HAZELCAST_ADDRESSES                         | string (list) | []                                                                                     | The addresses to connect to.                                                                                       |
| store.hazelcast.clusterName                             | QUASAR_STORE_HAZELCAST_CLUSTERNAME                       | string        | horizon                                                                                | Hazelcast cluster to write to.                                                                                     |
| store.hazelcast.username                                | QUASAR_STORE_HAZELCAST_USERNAME                          | string        | -                                                                                      | Username to authenticate with.                                                                                     |
| store.hazelcast.password                                | QUASAR_STORE_HAZELCAST_PASSWORD                          | string        | -                                                                                      | Password to authenticate with.                                                                                     |
| store.hazelcast.unisocket                               | QUASAR_STORE_HAZELCAST_UNISOCKET                         | bool          | false                                                                                  | Connect to Hazelcast in unisocket mode.                                                                            |
| store.hazelcast.reconcileMode                           | QUASAR_STORE_HAZELCAST_RECONCILEMODE                     | string        | full                                                                                   | Perform incremental or full reconcile (accepted values: full/incremental).                                         |
| store.hazelcast.reconciliationInterval                  | QUASAR_STORE_HAZELCAST_RECONCILIATIONINTERVAL            | string        | 60s                                                                                    | Interval for the periodic reconciliation (minimum: 60s).                                                           |
| store.hazelcast.heartbeatTimeout                        | QUASAR_STORE_HAZELCAST_HEARTBEATTIMEOUT                  | string        | 30s                                                                                    | Maximum waiting time for responses to heartbeat pings.                                                             |
| store.hazelcast.connectionTimeout                       | QUASAR_STORE_HAZELCAST_CONNECTIONTIMEOUT                 | string        | 30s                                                                                    | Connection timeout for Hazelcast client.                                                                           |
| store.hazelcast.invocationTimeout                       | QUASAR_STORE_HAZELCAST_INVOCATIONTIMEOUT                 | string        | 60s                                                                                    | Invocation timeout for Hazelcast operations.                                                                       |
| store.hazelcast.redoOperation                           | QUASAR_STORE_HAZELCAST_REDOOPERATION                     | bool          | false                                                                                  | Whether to redo (idempotent) operations on failure.                                                                |
| store.hazelcast.connectionStrategy.reconnectMode        | QUASAR_STORE_HAZELCAST_CONNECTIONSTRATEGY_RECONNECTMODE  | string        | on                                                                                     | Hazelcast reconnection mode.                                                                                       |
| store.hazelcast.connectionStrategy.timeout              | QUASAR_STORE_HAZELCAST_CONNECTIONSTRATEGY_TIMEOUT        | string        | 10m                                                                                    | Timeout for Hazelcast connection strategy.                                                                         |
| store.hazelcast.connectionStrategy.retry.initialbackoff | QUASAR_STORE_HAZELCAST_CONNECTIONSTRATEGY_RETRY_INITIALBACKOFF | string  | 1s                                                                                     | Initial backoff for Hazelcast reconnection retries.                                                                |
| store.hazelcast.connectionStrategy.retry.jitter         | QUASAR_STORE_HAZELCAST_CONNECTIONSTRATEGY_RETRY_JITTER   | float         | 0.0                                                                                    | Jitter factor for Hazelcast reconnection retries.                                                                  |
| store.hazelcast.connectionStrategy.retry.maxbackoff     | QUASAR_STORE_HAZELCAST_CONNECTIONSTRATEGY_RETRY_MAXBACKOFF | string    | 10s                                                                                    | Maximum backoff for Hazelcast reconnection retries.                                                                |
| store.hazelcast.connectionStrategy.retry.multiplier     | QUASAR_STORE_HAZELCAST_CONNECTIONSTRATEGY_RETRY_MULTIPLIER | float     | 1.2                                                                                    | Multiplier for backoff increase on Hazelcast reconnection.                                                         |
| store.mongo.uri                                         | QUASAR_STORE_MONGO_URI                                   | string        | mongodb://localhost:27017                                                              | MongoDB uri of the database.                                                                                       |
| store.mongo.database                                    | QUASAR_STORE_MONGO_DATABASE                              | string        | horizon-config                                                                         | The database that should be written to.                                                                            |
| store.redis.host                                        | QUASAR_STORE_REDIS_HOST                                  | string        | localhost                                                                              | The redis host.                                                                                                    |
| store.redis.port                                        | QUASAR_STORE_REDIS_PORT                                  | int           | 6379                                                                                   | The redis port.                                                                                                    |
| store.redis.username                                    | QUASAR_STORE_REDIS_USERNAME                              | string        | -                                                                                      | Username to authenticate with.                                                                                     |
| store.redis.password                                    | QUASAR_STORE_REDIS_PASSWORD                              | string        | -                                                                                      | Password to authenticate with.                                                                                     |
| store.redis.database                                    | QUASAR_STORE_REDIS_DATABASE                              | int           | 0                                                                                      | Redis database index.                                                                                              |
| watcher.store.primary.type                              | QUASAR_WATCHER_STORE_PRIMARY_TYPE                        | string        | hazelcast                                                                              | Primary store type for the watcher (hazelcast, mongo, redis).                                                      |
| watcher.store.secondary.type                            | QUASAR_WATCHER_STORE_SECONDARY_TYPE                      | string        | mongo                                                                                  | Secondary store type for the watcher (hazelcast, mongo, redis).                                                    |
| provisioning.port                                       | QUASAR_PROVISIONING_PORT                                 | int           | 8081                                                                                   | The port for the provisioning API service.                                                                         |
| provisioning.logLevel                                   | QUASAR_PROVISIONING_LOGLEVEL                             | string        | info                                                                                   | The log-level for the provisioning service.                                                                        |
| provisioning.store.primary.type                         | QUASAR_PROVISIONING_STORE_PRIMARY_TYPE                   | string        | mongo                                                                                  | Primary store type for provisioning (hazelcast, mongo, redis).                                                     |
| provisioning.store.secondary.type                       | QUASAR_PROVISIONING_STORE_SECONDARY_TYPE                 | string        | hazelcast                                                                              | Secondary store type for provisioning (hazelcast, mongo, redis).                                                   |
| provisioning.security.enabled                           | QUASAR_PROVISIONING_SECURITY_ENABLED                     | bool          | true                                                                                   | Whether or not security should be enabled for the provisioning API.                                                |
| provisioning.security.trustedIssuers                    | QUASAR_PROVISIONING_SECURITY_TRUSTEDISSUERS              | string (list) | ["https://auth.example.com/certs"]                                                     | List of trusted JWT issuers for authentication.                                                                    |
| provisioning.security.trustedClients                    | QUASAR_PROVISIONING_SECURITY_TRUSTEDCLIENTS              | string (list) | ["example-client"]                                                                     | List of trusted client IDs for authentication.                                                                     |
| metrics.enabled                                         | QUASAR_METRICS_ENABLED                                   | bool          | false                                                                                  | Whether or not metrics should be served.                                                                           |
| metrics.port                                            | QUASAR_METRICS_PORT                                      | int           | 8080                                                                                   | The port for exposing the metrics service.                                                                         |
| metrics.timeout                                         | QUASAR_METRICS_TIMEOUT                                   | string        | 5s                                                                                     | Timeout of HTTP connections to the metrics service.                                                                |
| resources                                               | -                                                        | object (list) | []                                                                                     | The custom resources that should be synchronized. See [configuring resources](#configuring-resources) for details. |
| snapshot.enabled                                        | QUASAR_SNAPSHOT_ENABLED                                  | bool          | false                                                                                  | Enable the subscription snapshot worker.                                                                           |
| snapshot.rebuildInterval                                | QUASAR_SNAPSHOT_REBUILDINTERVAL                          | string        | 120s                                                                                   | Interval for checking for changes and rebuilding the snapshot.                                                     |
| snapshot.activationDelay                                | QUASAR_SNAPSHOT_ACTIVATIONDELAY                          | string        | 60s                                                                                    | Wait between preparing and activating a snapshot in ZooKeeper.                                                     |
| snapshot.cleanupInterval                                | QUASAR_SNAPSHOT_CLEANUPINTERVAL                          | string        | 300s                                                                                   | Interval for deleting stale snapshots.                                                                             |
| snapshot.mongo.uri                                      | QUASAR_SNAPSHOT_MONGO_URI                                | string        | mongodb://localhost:27017,localhost:27018,localhost:27019/?replicaSet=rs0              | MongoDB uri of the snapshot database. Inject credentials through a secret.                                         |
| snapshot.mongo.database                                 | QUASAR_SNAPSHOT_MONGO_DATABASE                           | string        | horizon-config                                                                         | MongoDB database containing source and snapshot collections.                                                       |
| snapshot.mongo.sourceCollection                         | QUASAR_SNAPSHOT_MONGO_SOURCECOLLECTION                   | string        | subscriptions.subscriber.horizon.telekom.de.v1                                         | MongoDB collection containing the source subscriptions.                                                            |
| snapshot.mongo.targetCollection                         | QUASAR_SNAPSHOT_MONGO_TARGETCOLLECTION                   | string        | subscriptions.subscriber.horizon.telekom.de.v1-snapshots                               | MongoDB collection the snapshots are written to.                                                                   |
| snapshot.mongo.reconnectInterval                        | QUASAR_SNAPSHOT_MONGO_RECONNECTINTERVAL                  | string        | 10s                                                                                    | Wait between change-stream reconnect attempts.                                                                     |
| snapshot.zookeeper.addresses                            | QUASAR_SNAPSHOT_ZOOKEEPER_ADDRESSES                      | string (list) | ["localhost:2181", "localhost:2182", "localhost:2183"]                                 | ZooKeeper host:port addresses. ENV-only lists are comma-separated.                                                 |
| snapshot.zookeeper.sessionTimeout                       | QUASAR_SNAPSHOT_ZOOKEEPER_SESSIONTIMEOUT                 | string        | 10s                                                                                    | Requested ZooKeeper session timeout.                                                                               |

### Subscription snapshots

Quasar stores complete subscription snapshots in MongoDB so consumers can preload
a new cache and switch to it once activated.
ZooKeeper addresses must be configured when enabled, but Quasar can start while
ZooKeeper is unavailable.

```yaml
snapshot:
  enabled: true
  rebuildInterval: 120s
  activationDelay: 60s
  cleanupInterval: 300s
  mongo:
    uri: "" # MongoDB URI, including authentication; inject through a secret.
    database: "{env}-horizon-config"
    sourceCollection: subscriptions.subscriber.horizon.telekom.de.v1
    targetCollection: subscriptions.subscriber.horizon.telekom.de.v1-snapshots
    reconnectInterval: 10s
  zookeeper:
    addresses: ["127.0.0.1:2181", "127.0.0.1:2182", "127.0.0.1:2183"]
    sessionTimeout: 10s
```

**Startup and publication**

```text
Quasar starts
  -> connect to MongoDB + ZooKeeper
  -> initialize or fetch active snapshot head
  -> watch MongoDB change stream for subscription mutations
  -> on change / rebuild interval: create snapshot in MongoDB
  -> write prepared snapshot metadata to ZooKeeper (/horizon/subscriptions/prepared)
  -> wait activationDelay (default: 60s)
  -> write activated snapshot metadata to ZooKeeper (/horizon/subscriptions/activated)
```

Missing ZooKeeper sessions or failed ZooKeeper operations enable MongoDB fallback.
ZooKeeper connection attempts continue in the background.

ZooKeeper contains snapshot metadata, not subscriptions. For consumers:
- `prepared` -> load the snapshot from MongoDB; keep using the current cache.
- `activated` -> switch to the new cache only once loading is complete.

Quasar does not wait for consumers to finish loading.

**ZooKeeper recovers**

```text
ZooKeeper is available again
  -> finish an interrupted update, if needed
  -> latest active MongoDB snapshot: prepared -> wait activationDelay -> activated
  -> continue normal operation
```

Recovery does not create another snapshot or replay every missed version.

**Important**

- Snapshot rebuild checks run periodically at `rebuildInterval` (default: 120s) when changes are detected via MongoDB change streams.
- Cleanup runs periodically at `cleanupInterval` (default: 300s) and protects active and recent historical snapshots referenced in ZooKeeper.
- The API or Kubernetes watcher can become ready before the first snapshot is published.
- If the initial MongoDB connection fails, Quasar stops. Later MongoDB change-stream disconnections retry at `reconnectInterval`.
- Run one replica with `Recreate` and no autoscaling; multiple publishers are unsupported.

See [architecture and consumer contract](docs/snapshots.md) for
publication and consumer fallback details.

### Configuring resources
The `resources` configuration option is a list of custom resources that should be synchronized. Each resource has the following fields:
```yaml
kubernetes:
  group: mygroup
  resource: myresource
  version: v1
  kind: MyKind
  namespace: mynamespace
mongoId: spec.myIdField
prometheus:
  enabled: true
  labels:
    fixed_value: foobar
    my_dynamic_value: $spec.myfield
mongoIndexes:
  - spec.myfield: 1
  - spec.myfield: 1
    spec.myotherfield: 1
hazelcastIndexes:
  - name: myfield_myotherfield_idx
    fields:
      - data.spec.environment
    type: sorted
```

#### Understanding resources
- `kubernetes`: The Kubernetes resource that should be synchronized.
  - `group`: The group of the resource.
  - `resource`: The name of the resource.
  - `version`: The version of the resource.
  - `kind`: The kind of the resource (used for GroupVersionKind matching).
  - `namespace`: The namespace of the resource.
- `mongoId`: The resource field path to map as the MongoDB document `_id` (falls back to the object UID if omitted).
- `prometheus`: Prometheus metrics configuration.
  - `enabled`: Whether to expose metrics for this resource.
  - `labels`: Labels that should be exposed as metrics. Labels can be fixed values or values from the resource.
    - Fixed values are defined as strings.
    - Values from the resource are defined as `$<field>`.
- `mongoIndexes`: Indexes that should be created in the MongoDB database.
- `hazelcastIndexes`: Indexes that should be created in the Hazelcast cache.
  - `name`: The name of the index.
  - `fields`: The fields that should be indexed.
  - `type`: The type of the index. Currently, only `sorted` and `hash` are supported.

#### Generating a local configuration
You can generate a local configuration file by running the following command in the directory of the executable:
```bash
./quasar init
```

## Running Quasar
Once you have prepared your configuration, you can run Quasar by executing the following command in the directory of the executable:
```bash
./quasar run
```

## Contributing
We're committed to open source, so we welcome and encourage everyone to join its developer community and contribute, whether it's through code or feedback.  
By participating in this project, you agree to abide by its [Code of Conduct](./CODE_OF_CONDUCT.md) at all times.

## Code of Conduct
This project has adopted the [Contributor Covenant](https://www.contributor-covenant.org/) in version 2.1 as our code of conduct. Please see the details in our [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md). All contributors must abide by the code of conduct.

By participating in this project, you agree to abide by its [Code of Conduct](./CODE_OF_CONDUCT.md) at all times.

## Licensing
This project follows the [REUSE standard for software licensing](https://reuse.software/).    
Each file contains copyright and license information, and license texts can be found in the [LICENSES](./LICENSES) folder. For more information visit https://reuse.software/. 
You can find a guide for developers at https://telekom.github.io/reuse-template/.   