// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/viper"
)

// MaxRetainedSnapshots limits the configured snapshot history.
const MaxRetainedSnapshots = 100

type SubscriptionSnapshots struct {
	Enabled                  bool              `mapstructure:"enabled"`
	URI                      string            `mapstructure:"uri"`
	Database                 string            `mapstructure:"database"`
	SourceCollection         string            `mapstructure:"sourceCollection"`
	SnapshotCollection       string            `mapstructure:"snapshotCollection"`
	HeadCollection           string            `mapstructure:"headCollection"`
	RefreshInterval          time.Duration     `mapstructure:"refreshInterval"`
	InitialRefreshDelay      time.Duration     `mapstructure:"initialRefreshDelay"`
	MinimumRetainedSnapshots int               `mapstructure:"minimumRetainedSnapshots"`
	RefreshTimeout           time.Duration     `mapstructure:"refreshTimeout"`
	CleanupTimeout           time.Duration     `mapstructure:"cleanupTimeout"`
	MaxSnapshotBytes         int64             `mapstructure:"maxSnapshotBytes"`
	ActivationDelay          time.Duration     `mapstructure:"activationDelay"`
	ZooKeeper                SnapshotZooKeeper `mapstructure:"zookeeper"`
}

type SnapshotZooKeeper struct {
	Addresses      []string      `mapstructure:"addresses"`
	BasePath       string        `mapstructure:"basePath"`
	SessionTimeout time.Duration `mapstructure:"sessionTimeout"`
}

func setSubscriptionSnapshotsDefaults() {
	const defaultTimeout = "60s"

	defaults := map[string]any{
		"enabled":                  false,
		"uri":                      "",
		"database":                 "",
		"sourceCollection":         "subscriptions.subscriber.horizon.telekom.de.v1",
		"snapshotCollection":       "subscriptions.subscriber.horizon.telekom.de.v1-snapshots",
		"headCollection":           "subscriptions.subscriber.horizon.telekom.de.v1-head",
		"refreshInterval":          "300s",
		"initialRefreshDelay":      "120s",
		"minimumRetainedSnapshots": 3,
		"refreshTimeout":           defaultTimeout,
		"cleanupTimeout":           defaultTimeout,
		"maxSnapshotBytes":         int64(64 * 1024 * 1024),
		"activationDelay":          "60s",
		"zookeeper.addresses":      []string{},
		"zookeeper.basePath":       "/horizon/subscriptions",
		"zookeeper.sessionTimeout": "10s",
	}
	for key, value := range defaults {
		// Defaults also make environment-only keys visible to Viper.Unmarshal.
		viper.SetDefault("subscriptionSnapshots."+key, value)
	}
}

func (c SubscriptionSnapshots) Validate() error {
	if !c.Enabled {
		return nil
	}
	if err := validateSubscriptionSnapshotsURI(c.URI); err != nil {
		return err
	}
	if c.Database == "" || len(c.Database) > 63 || strings.ContainsAny(c.Database, "/\\ .\"$*<>:|?\x00") {
		return errors.New("subscriptionSnapshots.database must be a valid, explicit MongoDB database name")
	}
	if err := c.validateCollections(); err != nil {
		return err
	}
	if c.RefreshInterval <= 0 || c.RefreshTimeout <= 0 || c.CleanupTimeout <= 0 {
		return errors.New("subscriptionSnapshots refreshInterval, refreshTimeout and cleanupTimeout must be positive")
	}
	if c.InitialRefreshDelay < 0 {
		return errors.New("subscriptionSnapshots.initialRefreshDelay must not be negative")
	}
	if c.MaxSnapshotBytes <= 0 {
		return errors.New("subscriptionSnapshots.maxSnapshotBytes must be positive")
	}
	if c.MinimumRetainedSnapshots < 3 || c.MinimumRetainedSnapshots > MaxRetainedSnapshots {
		return errors.New("subscriptionSnapshots.minimumRetainedSnapshots must be between 3 and 100")
	}
	return c.validateZooKeeper()
}

func (c SubscriptionSnapshots) validateZooKeeper() error {
	if c.ActivationDelay <= 0 || c.ZooKeeper.SessionTimeout <= 0 {
		return errors.New("subscriptionSnapshots activationDelay and zookeeper.sessionTimeout must be positive")
	}
	if len(c.ZooKeeper.Addresses) == 0 {
		return errors.New("subscriptionSnapshots.zookeeper.addresses must contain an explicit host:port address")
	}
	for _, address := range c.ZooKeeper.Addresses {
		if !validZooKeeperAddress(address) {
			return errors.New("subscriptionSnapshots.zookeeper.addresses must contain valid host:port addresses")
		}
	}
	if !validZooKeeperPath(c.ZooKeeper.BasePath) {
		return errors.New("subscriptionSnapshots.zookeeper.basePath must be a canonical absolute non-system ZooKeeper path")
	}
	return nil
}

func validZooKeeperAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n/@?#\\") {
		return false
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return false
	}
	if strings.Contains(host, ":") {
		host, _, _ = strings.Cut(host, "%")
		return net.ParseIP(host) != nil
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if char != '-' && (char < '0' || char > '9') && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') {
				return false
			}
		}
	}
	return true
}

func validZooKeeperPath(value string) bool {
	if !utf8.ValidString(value) || !strings.HasPrefix(value, "/") ||
		value == "/zookeeper" || strings.HasPrefix(value, "/zookeeper/") {

		return false
	}
	if value != "/" {
		for _, segment := range strings.Split(value[1:], "/") {
			if segment == "" || segment == "." || segment == ".." {
				return false
			}
		}
	}
	for _, char := range value {
		if char <= 0x1f || char >= 0x7f && char <= 0x9f ||
			char >= 0xd800 && char <= 0xf8ff || char >= 0xfff0 && char <= 0xffff {

			return false
		}
	}
	return true
}

func (c SubscriptionSnapshots) validateCollections() error {
	seen := make(map[string]bool, 3)
	for _, name := range []string{c.SourceCollection, c.SnapshotCollection, c.HeadCollection} {
		if name == "" || strings.ContainsAny(name, "$\x00") || strings.HasPrefix(name, "system.") ||
			strings.Contains(name, "..") || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".") ||
			len(c.Database)+len(name)+1 > 120 {

			return errors.New("subscriptionSnapshots collection names must be valid, non-system MongoDB namespaces")
		}
		if seen[name] {
			return errors.New("subscriptionSnapshots source, snapshot and head collections must be distinct")
		}
		seen[name] = true
	}
	return nil
}

func validateSubscriptionSnapshotsURI(uri string) error {
	if strings.TrimSpace(uri) == "" {
		return errors.New("subscriptionSnapshots.uri must be configured")
	}
	// Inspect only consistency options here; the driver parses the original URI when connecting.
	_, rawQuery, _ := strings.Cut(uri, "?")
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return errors.New("subscriptionSnapshots.uri contains invalid options")
	}
	for key, values := range query {
		for _, value := range values {
			if err := validateSubscriptionSnapshotsURIOption(strings.ToLower(key), strings.ToLower(value)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSubscriptionSnapshotsURIOption(key, value string) error {
	switch key {
	case "w":
		if value != "majority" {
			return errors.New("subscriptionSnapshots URI must not weaken majority write concern")
		}
	case "journal", "j":
		if value != "true" {
			return errors.New("subscriptionSnapshots URI must not disable journaled writes")
		}
	case "readconcernlevel":
		if value != "majority" {
			return errors.New("subscriptionSnapshots URI must not weaken majority read concern")
		}
	case "readpreference":
		if value != "primary" {
			return errors.New("subscriptionSnapshots URI must use primary reads")
		}
	case "readpreferencetags", "maxstalenessseconds":
		return errors.New("subscriptionSnapshots URI must not configure secondary reads")
	}
	return nil
}
