// Copyright 2024 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"context"
	"fmt"
	"github.com/hazelcast/hazelcast-go-client"
	"github.com/hazelcast/hazelcast-go-client/cluster"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"log"
	"time"
)

var (
	pool      *dockertest.Pool
	resources = make([]*dockertest.Resource, 0)

	hazelcastImage = EnvOrDefault("HAZELCAST_IMAGE", "hazelcast/hazelcast")
	hazelcastTag   = EnvOrDefault("HAZELCAST_TAG", "5.3.6")
	hazelcastHost  = EnvOrDefault("HAZELCAST_HOST", "localhost")
	hazelcastPort  = EnvOrDefault("HAZELCAST_PORT", "5701")

	mongoImage = EnvOrDefault("MONGO_IMAGE", "mongo")
	mongoTag   = EnvOrDefault("MONGO_TAG", "7.0.5-rc0")
	mongoHost  = EnvOrDefault("MONGO_HOST", "localhost")
	mongoPort  = EnvOrDefault("MONGO_PORT", "27017")

	alreadySetUp bool = false
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

	// MongoDB
	if opts.MongoDb {
		if err := setupMongoDb(); err != nil {
			log.Fatalf("Could not setup mongodb: %s", err)
		}
	}

	// Hazelcast
	if opts.Hazelcast {
		if err := setupHazelcast(); err != nil {
			log.Fatalf("Could not setup hazelcast: %s", err)
		}
	}

	// Wait no longer than 30 seconds
	pool.MaxWait = 30 * time.Second

	err = pool.Retry(func() error {

		// MongoDB readiness
		if opts.MongoDb {
			if err := pingMongoDb(); err != nil {
				return err
			}
		}
		log.Println("MongoDB is ready!")

		// Hazelcast rediness
		if opts.Hazelcast {
			if err := pingHazelcast(); err != nil {
				return err
			}
		}
		log.Println("Hazelcast is ready!")

		return nil
	})
	if err != nil {
		log.Fatalf("Readiness probe failed: %s", err)
	}

	alreadySetUp = true
}

func TeardownDocker() {
	for _, resource := range resources {
		if err := pool.Purge(resource); err != nil {
			log.Fatalf("Could not purge container: %s", err)
		}
	}
}

func pingMongoDb() error {
	var ctx = context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(fmt.Sprintf("mongodb://%s:%s", mongoHost, mongoPort)))
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
	var ctx = context.Background()
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
