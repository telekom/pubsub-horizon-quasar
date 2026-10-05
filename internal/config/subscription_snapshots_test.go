// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func validSubscriptionSnapshots() SubscriptionSnapshots {
	return SubscriptionSnapshots{
		Enabled: true, URI: "mongodb://localhost:27017", Database: "integration-horizon-config",
		SourceCollection: "source", SnapshotCollection: "snapshots", HeadCollection: "head",
		RefreshInterval:          5 * time.Minute,
		MinimumRetainedSnapshots: 3, RefreshTimeout: 2 * time.Minute, CleanupTimeout: 2 * time.Minute,
		MaxSnapshotBytes: 64 * 1024 * 1024,
		ActivationDelay:  time.Minute,
		ZooKeeper: SnapshotZooKeeper{
			Addresses: []string{"localhost:2181"}, BasePath: "/horizon/subscriptions", SessionTimeout: 10 * time.Second,
		},
	}
}

func TestSubscriptionSnapshotsValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*SubscriptionSnapshots)
		valid  bool
	}{
		{"defaults", func(*SubscriptionSnapshots) {}, true},
		{"disabled", func(c *SubscriptionSnapshots) { *c = SubscriptionSnapshots{} }, true},
		{"missing URI", func(c *SubscriptionSnapshots) { c.URI = "" }, false},
		{"blank URI", func(c *SubscriptionSnapshots) { c.URI = " \t" }, false},
		{"scheme validation deferred to driver", func(c *SubscriptionSnapshots) { c.URI = "https://localhost" }, true},
		{"URI credentials", func(c *SubscriptionSnapshots) { c.URI = "mongodb://user:secret@localhost" }, true},
		{"URI auth source", func(c *SubscriptionSnapshots) { c.URI += "/?authSource=admin" }, true},
		{"URI authentication mechanism", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb://user:secret@localhost/?authSource=users&authMechanism=SCRAM-SHA-256"
		}, true},
		{"escaped URI credentials", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb://user:pa%40ss%3Aword@localhost/?authSource=admin"
		}, true},
		{"invalid credential escape", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb://user:secret%ZZ@localhost"
		}, true},
		{"credentials do not weaken concerns", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb://user:secret@localhost/?authSource=admin&w=0"
		}, false},
		{"unacknowledged writes", func(c *SubscriptionSnapshots) { c.URI += "/?w=0" }, false},
		{"weak writes", func(c *SubscriptionSnapshots) { c.URI += "/?w=1" }, false},
		{"no journal", func(c *SubscriptionSnapshots) { c.URI += "/?journal=false" }, false},
		{"journal alias", func(c *SubscriptionSnapshots) { c.URI += "/?j=false" }, false},
		{"duplicate concern", func(c *SubscriptionSnapshots) { c.URI += "/?w=majority&w=0" }, false},
		{"encoded concern", func(c *SubscriptionSnapshots) { c.URI += "/?%77=0" }, false},
		{"case insensitive concern", func(c *SubscriptionSnapshots) { c.URI += "/?READPREFERENCE=secondary" }, false},
		{"weak reads", func(c *SubscriptionSnapshots) { c.URI += "/?readConcernLevel=local" }, false},
		{"secondary reads", func(c *SubscriptionSnapshots) { c.URI += "/?readPreference=secondary" }, false},
		{"read tags", func(c *SubscriptionSnapshots) { c.URI += "/?readPreferenceTags=" }, false},
		{"staleness", func(c *SubscriptionSnapshots) { c.URI += "/?maxStalenessSeconds=90" }, false},
		{"unreadable options", func(c *SubscriptionSnapshots) { c.URI += "/?w=%zz" }, false},
		{"consistent URI", func(c *SubscriptionSnapshots) {
			c.URI += "/?w=majority&journal=true&readConcernLevel=majority&readPreference=primary"
		}, true},
		{"SRV", func(c *SubscriptionSnapshots) { c.URI = "mongodb+srv://example.invalid" }, true},
		{"SRV credentials without discovery", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb+srv://user:secret@example.invalid/?authSource=admin"
		}, true},
		{"SRV port deferred", func(c *SubscriptionSnapshots) { c.URI = "mongodb+srv://example.invalid:27017" }, true},
		{"SRV invalid option", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb+srv://example.invalid/?connectTimeoutMS=invalid"
		}, true},
		{"SRV invalid host limit", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb+srv://example.invalid/?srvMaxHosts=-1"
		}, true},
		{"SRV direct connection", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb+srv://example.invalid/?directConnection=true"
		}, true},
		{"SRV replica set host limit", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb+srv://example.invalid/?srvMaxHosts=1&replicaSet=test"
		}, true},
		{"SRV options without discovery", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb+srv://example.invalid/?srvServiceName=mongodb&srvMaxHosts=2&connectTimeoutMS=100"
		}, true},
		{"SRV weaker reads", func(c *SubscriptionSnapshots) {
			c.URI = "mongodb+srv://example.invalid/?readConcernLevel=local"
		}, false},
		{"missing database", func(c *SubscriptionSnapshots) { c.Database = "" }, false},
		{"invalid database", func(c *SubscriptionSnapshots) { c.Database = "bad/name" }, false},
		{"empty collection", func(c *SubscriptionSnapshots) { c.SourceCollection = "" }, false},
		{"system collection", func(c *SubscriptionSnapshots) { c.SnapshotCollection = "system.snapshots" }, false},
		{"collection collision", func(c *SubscriptionSnapshots) { c.HeadCollection = c.SourceCollection }, false},
		{"namespace too long", func(c *SubscriptionSnapshots) { c.HeadCollection = strings.Repeat("x", 120) }, false},
		{"refresh", func(c *SubscriptionSnapshots) { c.RefreshInterval = 0 }, false},
		{"refresh timeout", func(c *SubscriptionSnapshots) { c.RefreshTimeout = 0 }, false},
		{"cleanup timeout", func(c *SubscriptionSnapshots) { c.CleanupTimeout = 0 }, false},
		{"payload size", func(c *SubscriptionSnapshots) { c.MaxSnapshotBytes = 0 }, false},
		{"minimum history", func(c *SubscriptionSnapshots) { c.MinimumRetainedSnapshots = 2 }, false},
		{"zero history", func(c *SubscriptionSnapshots) { c.MinimumRetainedSnapshots = 0 }, false},
		{"negative minimum history", func(c *SubscriptionSnapshots) {
			c.MinimumRetainedSnapshots = -1
		}, false},
		{"maximum retained snapshots", func(c *SubscriptionSnapshots) {
			c.MinimumRetainedSnapshots = MaxRetainedSnapshots
		}, true},
		{"history bound", func(c *SubscriptionSnapshots) {
			c.MinimumRetainedSnapshots = MaxRetainedSnapshots + 1
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validSubscriptionSnapshots()
			tt.change(&c)
			err := c.Validate()
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), "secret")
			}
		})
	}
}

func TestSubscriptionSnapshotsEnvironmentOnly(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	viper.AutomaticEnv()
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_ENABLED", "true")
	uri := "mongodb://snapshot-user:test-password@other-host:27017/?authSource=snapshot-auth"
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_URI", uri)
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_DATABASE", "test-horizon-config")
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_REFRESHINTERVAL", "9m")
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_REFRESHTIMEOUT", "3m")
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_ACTIVATIONDELAY", "45s")
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_ZOOKEEPER_ADDRESSES", "host-a:2181,[::1]:2182")
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_ZOOKEEPER_BASEPATH", "/test/subscriptions")
	t.Setenv("QUASAR_SUBSCRIPTIONSNAPSHOTS_ZOOKEEPER_SESSIONTIMEOUT", "12s")
	var c Configuration
	require.NoError(t, viper.Unmarshal(&c))
	require.NoError(t, c.SubscriptionSnapshots.Validate())
	require.Equal(t, uri, c.SubscriptionSnapshots.URI)
	require.Equal(t, "test-horizon-config", c.SubscriptionSnapshots.Database)
	clientOptions := options.Client().ApplyURI(c.SubscriptionSnapshots.URI)
	require.NotNil(t, clientOptions.Auth)
	require.Equal(t, "snapshot-user", clientOptions.Auth.Username)
	require.Equal(t, "test-password", clientOptions.Auth.Password)
	require.Equal(t, "snapshot-auth", clientOptions.Auth.AuthSource)
	require.Equal(t, 9*time.Minute, c.SubscriptionSnapshots.RefreshInterval)
	require.Equal(t, 3*time.Minute, c.SubscriptionSnapshots.RefreshTimeout)
	require.Equal(t, 45*time.Second, c.SubscriptionSnapshots.ActivationDelay)
	require.Equal(t, []string{"host-a:2181", "[::1]:2182"}, c.SubscriptionSnapshots.ZooKeeper.Addresses)
	require.Equal(t, "/test/subscriptions", c.SubscriptionSnapshots.ZooKeeper.BasePath)
	require.Equal(t, 12*time.Second, c.SubscriptionSnapshots.ZooKeeper.SessionTimeout)
	require.Equal(t, 3, c.SubscriptionSnapshots.MinimumRetainedSnapshots)
	require.Equal(t, time.Minute, c.SubscriptionSnapshots.CleanupTimeout)
	require.Equal(t, int64(64*1024*1024), c.SubscriptionSnapshots.MaxSnapshotBytes)
	require.Equal(t, "mongodb://localhost:27017", c.Store.Mongo.Uri)
	require.Equal(t, "mongodb://localhost:27017", c.Fallback.Mongo.Uri)
}

func TestSubscriptionSnapshotsDisabledDefaults(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	setDefaults()
	var c Configuration
	require.NoError(t, viper.Unmarshal(&c))
	require.False(t, c.SubscriptionSnapshots.Enabled)
	require.Empty(t, c.SubscriptionSnapshots.URI)
	require.Empty(t, c.SubscriptionSnapshots.Database)
	require.Empty(t, c.SubscriptionSnapshots.ZooKeeper.Addresses)
	require.Equal(t, time.Minute, c.SubscriptionSnapshots.ActivationDelay)
	require.Equal(t, "/horizon/subscriptions", c.SubscriptionSnapshots.ZooKeeper.BasePath)
	require.Equal(t, 10*time.Second, c.SubscriptionSnapshots.ZooKeeper.SessionTimeout)
	require.NoError(t, c.SubscriptionSnapshots.Validate())
}

func TestSubscriptionSnapshotsZooKeeperValidation(t *testing.T) {
	tests := []struct {
		name   string
		change func(*SubscriptionSnapshots)
		valid  bool
	}{
		{"missing addresses", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = nil }, false},
		{"IPv6", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"[::1]:2181"} }, true},
		{"multiple servers", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"a:2181", "b:2182"} }, true},
		{"missing host", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{":2181"} }, false},
		{"missing port", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"localhost"} }, false},
		{"URL", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"http://localhost:2181"} }, false},
		{"credentials", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"user@localhost:2181"} }, false},
		{"zero port", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"localhost:0"} }, false},
		{"overflow port", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"localhost:65536"} }, false},
		{"invalid hostname", func(c *SubscriptionSnapshots) { c.ZooKeeper.Addresses = []string{"a b:2181"} }, false},
		{"relative path", func(c *SubscriptionSnapshots) { c.ZooKeeper.BasePath = "horizon/subscriptions" }, false},
		{"empty segment", func(c *SubscriptionSnapshots) { c.ZooKeeper.BasePath = "/horizon//subscriptions" }, false},
		{"trailing slash", func(c *SubscriptionSnapshots) { c.ZooKeeper.BasePath = "/horizon/" }, false},
		{"dot segment", func(c *SubscriptionSnapshots) { c.ZooKeeper.BasePath = "/horizon/../subscriptions" }, false},
		{"system path", func(c *SubscriptionSnapshots) { c.ZooKeeper.BasePath = "/zookeeper/test" }, false},
		{"control character", func(c *SubscriptionSnapshots) { c.ZooKeeper.BasePath = "/horizon/\x00" }, false},
		{"root", func(c *SubscriptionSnapshots) { c.ZooKeeper.BasePath = "/" }, true},
		{"zero delay", func(c *SubscriptionSnapshots) { c.ActivationDelay = 0 }, false},
		{"negative delay", func(c *SubscriptionSnapshots) { c.ActivationDelay = -time.Second }, false},
		{"zero session timeout", func(c *SubscriptionSnapshots) { c.ZooKeeper.SessionTimeout = 0 }, false},
		{"short refresh budget", func(c *SubscriptionSnapshots) { c.RefreshTimeout = time.Second }, true},
		{"disabled invalid values", func(c *SubscriptionSnapshots) { *c = SubscriptionSnapshots{} }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validSubscriptionSnapshots()
			tt.change(&c)
			err := c.Validate()
			if tt.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
