// Copyright 2024-2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"context"
	"log"
	"net"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hazelcast/hazelcast-go-client"
	"github.com/hazelcast/hazelcast-go-client/cluster"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

const allInterfaces = "0.0.0.0"

var (
	pool      *dockertest.Pool
	resources = make([]*dockertest.Resource, 0)

	hazelcastImage = EnvOrDefault("HAZELCAST_IMAGE", "hazelcast/hazelcast")
	hazelcastTag   = EnvOrDefault("HAZELCAST_TAG", "5.3.6")
	hazelcastHost  = EnvOrDefault("HAZELCAST_HOST", allInterfaces)
	hazelcastPort  = EnvOrDefault("HAZELCAST_PORT", "5701")

	mongoImage = EnvOrDefault("MONGO_IMAGE", "mongo")
	mongoTag   = EnvOrDefault("MONGO_TAG", "7.0.5-rc0")
	mongoHost  = EnvOrDefault("MONGO_HOST", allInterfaces)
	mongoPort  = EnvOrDefault("MONGO_PORT", "27017")

	zookeeperImage = EnvOrDefault("ZOOKEEPER_IMAGE", "zookeeper")
	zookeeperTag   = EnvOrDefault("ZOOKEEPER_TAG", "3.9.5-jre-17")

	alreadySetUp = false
)

type Options struct {
	MongoDb   bool
	Hazelcast bool
}

func SetupDocker(opts *Options) {
	if alreadySetUp {
		return
	}

	log.Println("Setting up docker (missing images will be pulled, which might take some time)...")

	initializePool()
	setupServices(opts)
	waitForServicesReady(opts)

	alreadySetUp = true
}

func initializePool() {
	var err error
	if pool == nil {
		pool, err = dockertest.NewPool("")
		if err != nil {
			log.Fatalf("Could not create pool: %s", err)
		}
	}

	if err := pool.Client.Ping(); err != nil {
		log.Fatalf("Could not ping docker: %s", err)
	}
}

func setupServices(opts *Options) {
	if opts.MongoDb {
		if err := setupMongoDb(); err != nil {
			log.Fatalf("Could not setup mongodb: %s", err)
		}
	}

	if opts.Hazelcast {
		if err := setupHazelcast(); err != nil {
			log.Fatalf("Could not setup hazelcast: %s", err)
		}
	}
}

func waitForServicesReady(opts *Options) {
	pool.MaxWait = 30 * time.Second

	err := pool.Retry(func() error {
		if opts.MongoDb {
			if err := pingMongoDb(); err != nil {
				return err
			}
		}

		if opts.Hazelcast {
			if err := pingHazelcast(); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		log.Fatalf("Readiness probe failed: %s", err)
	}

	if opts.MongoDb {
		log.Println("MongoDB is ready!")
	}
	if opts.Hazelcast {
		log.Println("Hazelcast is ready!")
	}
}

func TeardownDocker() {
	for _, resource := range resources {
		if err := pool.Purge(resource); err != nil {
			log.Fatalf("Could not purge container: %s", err)
		}
	}
}

func pingMongoDb() error {
	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI("mongodb://"+net.JoinHostPort(mongoHost, mongoPort)))
	if err != nil {
		log.Printf("Could not reach mongodb: %s\n", err)
		return err
	}

	return client.Ping(ctx, nil)
}

func setupMongoDb() error {
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Name:         "quasar-mongodb",
		Repository:   mongoImage,
		Tag:          mongoTag,
		ExposedPorts: []string{"27017/tcp"},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"27017/tcp": {{HostIP: mongoHost, HostPort: mongoPort}},
		},
	}, configureTeardown)
	resources = append(resources, resource)
	return err
}

// SetupMongoReplicaSet starts three journaled MongoDB members and returns their replica-set URI.
// It waits for a primary and removes only its own container when the test ends.
func SetupMongoReplicaSet(t *testing.T) string {
	t.Helper()
	replicaPool, err := dockertest.NewPool("")
	require.NoError(t, err)
	require.NoError(t, replicaPool.Client.Ping())
	ports := reserveMongoPorts(t)
	host := dockerTestHost("MONGO_HOST")
	bindings := make(map[docker.Port][]docker.PortBinding, len(ports))
	var exposed, commands, addresses []string
	// Matching internal and published ports let clients discover every member through the Docker host.
	for i, port := range ports {
		exposed = append(exposed, port+"/tcp")
		bindings[docker.Port(port+"/tcp")] = []docker.PortBinding{{HostIP: allInterfaces, HostPort: port}}
		path := "/data/member" + strconv.Itoa(i)
		commands = append(commands, "mkdir -p "+path+"; mongod --bind_ip_all --port "+port+
			" --dbpath "+path+" --replSet quasar-test --setParameter enableTestCommands=1 --logpath "+path+".log &")
		addresses = append(addresses, net.JoinHostPort(host, port))
	}
	var extraHosts []string
	if net.ParseIP(host) == nil && host != "host.docker.internal" {
		extraHosts = []string{host + ":host-gateway"}
	}
	resource, err := replicaPool.RunWithOptions(&dockertest.RunOptions{
		Repository: mongoImage, Tag: mongoTag, ExposedPorts: exposed, PortBindings: bindings,
		Cmd:        []string{"bash", "-c", strings.Join(commands, "\n") + "\nwait"},
		ExtraHosts: extraHosts,
	}, configureTeardown)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, replicaPool.Purge(resource)) })

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	direct, err := mongo.Connect(ctx, options.Client().
		ApplyURI("mongodb://"+addresses[0]+"/?directConnection=true").SetServerSelectionTimeout(time.Second))
	require.NoError(t, err)
	defer func() { require.NoError(t, direct.Disconnect(context.Background())) }()
	replicaPool.MaxWait = 60 * time.Second
	require.NoError(t, replicaPool.Retry(func() error {
		return direct.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Err()
	}))
	members := make(bson.A, 0, len(addresses))
	for i, address := range addresses {
		members = append(members, bson.D{{Key: "_id", Value: i}, {Key: "host", Value: address}})
	}
	require.NoError(t, direct.Database("admin").RunCommand(ctx, bson.D{{Key: "replSetInitiate", Value: bson.D{
		{Key: "_id", Value: "quasar-test"}, {Key: "members", Value: members},
	}}}).Err())
	uri := "mongodb://" + strings.Join(addresses, ",") + "/?replicaSet=quasar-test"
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(time.Second))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Disconnect(context.Background())) }()
	require.NoError(t, replicaPool.Retry(func() error { return client.Ping(ctx, readpref.Primary()) }))
	return uri
}

// reserveMongoPorts selects three distinct free loopback ports and releases them before Docker starts.
func reserveMongoPorts(t *testing.T) []string {
	t.Helper()
	var listeners []net.Listener
	var ports []string
	var listenConfig net.ListenConfig
	for range 3 {
		listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)
		listeners = append(listeners, listener)
		_, port, err := net.SplitHostPort(listener.Addr().String())
		require.NoError(t, err)
		ports = append(ports, port)
	}
	for _, listener := range listeners {
		require.NoError(t, listener.Close())
	}
	return ports
}

func setupHazelcast() error {
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Name:         "quasar-hazelcast",
		Repository:   hazelcastImage,
		Tag:          hazelcastTag,
		ExposedPorts: []string{"5701/tcp"},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"5701/tcp": {{HostIP: hazelcastHost, HostPort: hazelcastPort}},
		},
		Env: []string{
			"HZ_CLUSTERNAME=horizon",
		},
	}, configureTeardown)
	resources = append(resources, resource)
	return err
}

func pingHazelcast() error {
	ctx := context.Background()
	config := hazelcast.NewConfig()

	config.Cluster.Name = "horizon"
	config.Cluster.Network.SetAddresses(hazelcastHost)
	config.Cluster.ConnectionStrategy.ReconnectMode = cluster.ReconnectModeOff

	config.Failover.TryCount = 5

	client, err := hazelcast.StartNewClientWithConfig(ctx, config)
	if err != nil {
		log.Printf("Could not connect to hazelcast: %s\n", err)
		return err
	}

	return client.Shutdown(ctx)
}

func configureTeardown(config *docker.HostConfig) {
	config.AutoRemove = true
	config.RestartPolicy = docker.RestartPolicy{
		Name: "no",
	}
}

// dockerTestHost selects an explicit service host, a remote Docker host or local loopback for test clients.
func dockerTestHost(variable string) string {
	if host := EnvOrDefault(variable, ""); host != "" && host != allInterfaces {
		return host
	}
	if endpoint, err := url.Parse(EnvOrDefault("DOCKER_HOST", "")); err == nil &&
		(endpoint.Scheme == "tcp" || endpoint.Scheme == "http" || endpoint.Scheme == "https") {

		return endpoint.Hostname()
	}
	return "127.0.0.1"
}
