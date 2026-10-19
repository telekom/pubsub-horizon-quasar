// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package zoo

import (
	"errors"
	"testing"
	"time"

	"github.com/Shopify/zk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/telekom/quasar/internal/test"
)

func TestClient_NewClient(t *testing.T) {
	t.Run("ReturnsImmediatelyWithoutBlockingOnConnectivity", func(t *testing.T) {
		start := time.Now()
		client, err := NewClient([]string{"127.0.0.1:1"}, time.Second)
		elapsed := time.Since(start)

		require.NoError(t, err)
		require.NotNil(t, client)
		assert.Less(t, elapsed, time.Second, "NewClient must not block waiting for connectivity")
	})

	t.Run("EmptyAddressesReturnsError", func(t *testing.T) {
		client, err := NewClient(nil, time.Second)

		assert.Error(t, err)
		assert.Nil(t, client)
	})
}

// TestClient_Ensemble provisions one shared three-member ZooKeeper ensemble and runs all
// ensemble-dependent scenarios as sub-tests so the ensemble is only started once.
func TestClient_Ensemble(t *testing.T) {
	ensemble := test.SetupZooKeeper(t)

	client, err := NewClient(ensemble.Addresses, 10*time.Second)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := client.Read("/")
		return err == nil
	}, 30*time.Second, 100*time.Millisecond)

	headPath := "/" + ZNodeHead
	preparePath := "/" + ZNodePrepared
	activatePath := "/" + ZNodeActivated

	t.Run("WriteCreatesNestedZNodePath", func(t *testing.T) {
		require.NoError(t, client.Write(headPath, []byte("v1")))

		data, err := client.Read(headPath)
		require.NoError(t, err)
		assert.Equal(t, []byte("v1"), data)

		_, err = client.Read("/horizon")
		assert.NoError(t, err, "parent znode /horizon should have been created recursively")

		_, err = client.Read("/horizon/subscriptions")
		assert.NoError(t, err, "parent znode /horizon/subscriptions should have been created recursively")
	})

	t.Run("WriteUpdatesExistingZNode", func(t *testing.T) {
		require.NoError(t, client.Write(headPath, []byte("v2")))

		data, err := client.Read(headPath)
		require.NoError(t, err)
		assert.Equal(t, []byte("v2"), data)
	})

	t.Run("WriteReusesExistingParentNodes", func(t *testing.T) {
		require.NoError(t, client.Write(preparePath, []byte("prepared")))

		headData, err := client.Read(headPath)
		require.NoError(t, err)
		assert.Equal(t, []byte("v2"), headData)

		prepareData, err := client.Read(preparePath)
		require.NoError(t, err)
		assert.Equal(t, []byte("prepared"), prepareData)
	})

	t.Run("ReadMissingZNodeReturnsError", func(t *testing.T) {
		data, err := client.Read(activatePath)

		assert.Error(t, err)
		assert.True(t, errors.Is(err, zk.ErrNoNode))
		assert.Nil(t, data)
	})

	t.Run("ClientSurvivesEnsembleMemberFailure", func(t *testing.T) {
		currentServer := client.conn.Server()

		memberIndex := -1
		for i, address := range ensemble.Addresses {
			if address == currentServer {
				memberIndex = i
				break
			}
		}
		require.GreaterOrEqual(t, memberIndex, 0, "connected server %q must be one of the ensemble addresses", currentServer)

		ensemble.Pause(t, memberIndex)
		defer ensemble.Resume(t, memberIndex)

		require.Eventually(t, func() bool {
			if err := client.Write(headPath, []byte("v3")); err != nil {
				return false
			}
			data, err := client.Read(headPath)
			return err == nil && string(data) == "v3"
		}, 30*time.Second, 200*time.Millisecond, "client should fail over to a remaining quorum member")
	})

	t.Run("Disconnect", func(t *testing.T) {
		client.Disconnect()

		_, err := client.Read(headPath)
		assert.Error(t, err)

		err = client.Write(headPath, []byte("v4"))
		assert.Error(t, err)
	})
}
