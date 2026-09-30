// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// MaxSnapshotHistory bounds even the largest serialized head below MongoDB's 16 MiB limit.
const MaxSnapshotHistory = 10000

type SubscriptionSnapshots struct {
	Enabled                  bool          `mapstructure:"enabled"`
	URI                      string        `mapstructure:"uri"`
	Database                 string        `mapstructure:"database"`
	SourceCollection         string        `mapstructure:"sourceCollection"`
	SnapshotCollection       string        `mapstructure:"snapshotCollection"`
	HeadCollection           string        `mapstructure:"headCollection"`
	RefreshInterval          time.Duration `mapstructure:"refreshInterval"`
	CleanupInterval          time.Duration `mapstructure:"cleanupInterval"`
	RetentionTime            time.Duration `mapstructure:"retentionTime"`
	MinimumRetainedSnapshots int           `mapstructure:"minimumRetainedSnapshots"`
	OperationTimeout         time.Duration `mapstructure:"operationTimeout"`
	MaxSnapshotBytes         int64         `mapstructure:"maxSnapshotBytes"`
}

func setSubscriptionSnapshotsDefaults() {
	defaults := map[string]any{
		"enabled":                  false,
		"uri":                      "",
		"database":                 "",
		"sourceCollection":         "subscriptions.subscriber.horizon.telekom.de.v1",
		"snapshotCollection":       "subscriptions.subscriber.horizon.telekom.de.v1-snapshots",
		"headCollection":           "subscriptions.subscriber.horizon.telekom.de.v1-head",
		"refreshInterval":          "5m",
		"cleanupInterval":          "1h",
		"retentionTime":            "168h",
		"minimumRetainedSnapshots": 3,
		"operationTimeout":         "2m",
		"maxSnapshotBytes":         int64(64 * 1024 * 1024),
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
	if c.RefreshInterval <= 0 || c.CleanupInterval <= 0 || c.RetentionTime <= 0 || c.OperationTimeout <= 0 {
		return errors.New("subscriptionSnapshots intervals, retentionTime and operationTimeout must be positive")
	}
	if c.MaxSnapshotBytes <= 0 {
		return errors.New("subscriptionSnapshots.maxSnapshotBytes must be positive")
	}
	if c.MinimumRetainedSnapshots < 3 || c.MinimumRetainedSnapshots > MaxSnapshotHistory {
		return errors.New("subscriptionSnapshots.minimumRetainedSnapshots must be between 3 and 10000")
	}
	return nil
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
