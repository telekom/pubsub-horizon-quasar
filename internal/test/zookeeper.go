// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package test

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Shopify/zk"
	"github.com/google/uuid"
	"github.com/ory/dockertest/v3"
	"github.com/ory/dockertest/v3/docker"
	"github.com/stretchr/testify/require"
)

type ZooKeeperEnsemble struct {
	Addresses []string
	pool      *dockertest.Pool
	members   []*dockertest.Resource
}

// SetupZooKeeper creates an isolated, three-member ensemble and removes only its own resources.
func SetupZooKeeper(t *testing.T) *ZooKeeperEnsemble {
	t.Helper()
	pool, err := dockertest.NewPool("")
	require.NoError(t, err)
	require.NoError(t, pool.Client.Ping())
	network, err := pool.Client.CreateNetwork(docker.CreateNetworkOptions{
		Name: "quasar-zookeeper-" + uuid.NewString(), Driver: "bridge",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pool.Client.RemoveNetwork(network.ID)) })
	ensemble := &ZooKeeperEnsemble{pool: pool}
	host := dockerTestHost("ZOOKEEPER_HOST")
	prefix := "zk-" + uuid.NewString()
	var servers []string
	for i := range 3 {
		name := prefix + "-zoo" + strconv.Itoa(i+1)
		servers = append(servers, "server."+strconv.Itoa(i+1)+"="+name+":2888:3888;2181")
	}
	for i := range 3 {
		member, err := runZooKeeperMember(t, pool, &dockertest.RunOptions{
			Repository: zookeeperImage,
			Tag:        zookeeperTag,
			Hostname:   prefix + "-zoo" + strconv.Itoa(i+1), Name: prefix + "-zoo" + strconv.Itoa(i+1),
			NetworkID: network.ID,
			Env: []string{
				"ZOO_MY_ID=" + strconv.Itoa(i+1), "ZOO_SERVERS=" + strings.Join(servers, " "),
				"ZOO_TICK_TIME=500", "ZOO_INIT_LIMIT=20", "ZOO_SYNC_LIMIT=10", "ZOO_MAX_CLIENT_CNXNS=100",
			},
			ExposedPorts: []string{"2181/tcp"},
			PortBindings: map[docker.Port][]docker.PortBinding{"2181/tcp": {{HostIP: allInterfaces}}},
		})
		require.NoError(t, err, "configured ZooKeeper image must be accessible; do not skip this test")
		ensemble.members = append(ensemble.members, member)
		t.Cleanup(func() {
			container, err := pool.Client.InspectContainer(member.Container.ID)
			if err == nil && container.State.Paused {
				require.NoError(t, pool.Client.UnpauseContainer(member.Container.ID))
			}
			require.NoError(t, pool.Purge(member))
		})
		ensemble.Addresses = append(ensemble.Addresses, net.JoinHostPort(host, member.GetPort("2181/tcp")))
	}
	conn, events, err := zk.Connect(ensemble.Addresses, 3*time.Second)
	require.NoError(t, err)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range events {
		}
	}()
	t.Cleanup(func() { conn.Close(); <-drained })
	require.Eventually(t, func() bool { return conn.State() == zk.StateHasSession }, 60*time.Second, 20*time.Millisecond)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, _, err = conn.Get(ctx, "/")
	require.NoError(t, err)
	return ensemble
}

// runZooKeeperMember starts an isolated member and retries port collisions after removing failed containers.
func runZooKeeperMember(t *testing.T, pool *dockertest.Pool, options *dockertest.RunOptions) (*dockertest.Resource, error) {
	t.Helper()
	for attempt := 1; ; attempt++ {
		member, err := pool.RunWithOptions(options, configureTeardown)
		if err == nil {
			return member, nil
		}
		if cleanupErr := removeFailedZooKeeperMember(pool, options.Name); cleanupErr != nil {
			return nil, errors.Join(err, cleanupErr)
		}
		if !strings.Contains(err.Error(), "address already in use") || attempt == 5 {
			return nil, err
		}
		t.Logf("Docker port collision; retrying isolated ZooKeeper member (%d/5)", attempt)
	}
}

// removeFailedZooKeeperMember removes only the named fixture container, treating an absent container as clean.
func removeFailedZooKeeperMember(pool *dockertest.Pool, name string) error {
	container, err := pool.Client.InspectContainer(name)
	var missing *docker.NoSuchContainer
	if errors.As(err, &missing) {
		return nil
	}
	if err != nil {
		return err
	}
	return pool.Purge(&dockertest.Resource{Container: container})
}

// Pause suspends one of the fixture's members without destroying its persistent data.
func (e *ZooKeeperEnsemble) Pause(t *testing.T, member int) {
	t.Helper()
	require.NoError(t, e.pool.Client.PauseContainer(e.members[member].Container.ID))
}

// Resume restores one of the fixture's suspended members.
func (e *ZooKeeperEnsemble) Resume(t *testing.T, member int) {
	t.Helper()
	require.NoError(t, e.pool.Client.UnpauseContainer(e.members[member].Container.ID))
}
