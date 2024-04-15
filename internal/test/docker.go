//go:build testing

package test

import (
	"context"
	"github.com/hazelcast/hazelcast-go-client"
	"github.com/hazelcast/hazelcast-go-client/cluster"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"log"
	"os"
)

var (
	pool      *dockertest.Pool
	resources []*dockertest.Resource = make([]*dockertest.Resource, 0)

	hazelcastHost string = envOrDefault("HAZELCAST_HOST", "localhost")
	hazelcastPort string = envOrDefault("HAZELCAST_PORT", "5701")

	mongoHost string = envOrDefault("MONGO_HOST", "localhost")
	mongoPort string = envOrDefault("MONGO_PORT", "27017")

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

	err = pool.Retry(func() error {

		// MongoDB readiness
		if opts.MongoDb {
			if err := pingMongoDb(); err != nil {
				return err
			}
		}

		// Hazelcast rediness
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
	client, err := mongo.Connect(ctx, options.Client())
	if err != nil {
		return err
	}

	return client.Ping(ctx, nil)
}

func setupMongoDb() error {
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Name:         "quasar-mongodb",
		Repository:   envOrDefault("MONGO_IMAGE", "mongo"),
		Tag:          envOrDefault("MONGO_TAG", "7.0.5-rc0"),
		ExposedPorts: []string{"27017/tcp"},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"27017/tcp": {{HostIP: "localhost", HostPort: "27017"}},
		},
	}, configureTeardown)
	resources = append(resources, resource)
	return err
}

func setupHazelcast() error {
	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Name:         "quasar-hazelcast",
		Repository:   envOrDefault("HAZELCAST_IMAGE", "hazelcast/hazelcast"),
		Tag:          envOrDefault("HAZELCAST_TAG", "5.3.6"),
		ExposedPorts: []string{"5701/tcp"},
		PortBindings: map[docker.Port][]docker.PortBinding{
			"5701/tcp": {{HostIP: "localhost", HostPort: "5701"}},
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
	config.Cluster.ConnectionStrategy.ReconnectMode = cluster.ReconnectModeOff

	config.Failover.TryCount = 5

	client, err := hazelcast.StartNewClientWithConfig(ctx, config)
	if err != nil {
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

func envOrDefault(name string, fallback string) string {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	return value
}
