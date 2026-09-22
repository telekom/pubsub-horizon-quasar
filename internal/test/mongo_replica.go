// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// SetupMongoReplicaSet runs three journaled members in one isolated test container.
// Identical loopback ports inside and outside the container make discovery work on Windows and Linux.
func SetupMongoReplicaSet(t *testing.T) string {
	t.Helper()
	replicaPool, err := dockertest.NewPool("")
	require.NoError(t, err)
	require.NoError(t, replicaPool.Client.Ping())
	ports := reserveMongoPorts(t)
	bindings := make(map[docker.Port][]docker.PortBinding, len(ports))
	var exposed, commands, addresses []string
	for i, port := range ports {
		exposed = append(exposed, port+"/tcp")
		bindings[docker.Port(port+"/tcp")] = []docker.PortBinding{{HostIP: "127.0.0.1", HostPort: port}}
		path := "/data/member" + strconv.Itoa(i)
		commands = append(commands, "mkdir -p "+path+"; mongod --bind_ip_all --port "+port+
			" --dbpath "+path+" --replSet quasar-test --setParameter enableTestCommands=1 --logpath "+path+".log &")
		addresses = append(addresses, net.JoinHostPort("localhost", port))
	}
	resource, err := replicaPool.RunWithOptions(&dockertest.RunOptions{
		Repository: mongoImage, Tag: mongoTag, ExposedPorts: exposed, PortBindings: bindings,
		Cmd: []string{"bash", "-c", strings.Join(commands, "\n") + "\nwait"},
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
