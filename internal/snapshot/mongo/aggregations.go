// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package mongo

import "go.mongodb.org/mongo-driver/bson"

var (
	// Aggregates all subscriptions into the snapshot format without an ObjectId.
	// The ObjectId must be added locally as the $createObjectId expression is unsupported in the currently used version of MongoDB (8.0).
	aggregateSnapshot = bson.A{
		bson.D{
			{"$set",
				bson.D{
					{"_snapshot._id", "not_set"},
					{"_snapshot.snapshotId", "not_set"},
					{"_snapshot.subscriptionId", "$_id"},
					{"_snapshot.resource", bson.D{}},
				},
			},
		},
		bson.D{
			{"$set",
				bson.D{
					{"_snapshot.resource.kind", "$kind"},
					{"_snapshot.resource.apiVersion", "$apiVersion"},
					{"_snapshot.resource.metadata", "$metadata"},
					{"_snapshot.resource.spec", "$spec"},
				},
			},
		},
		bson.D{{"$replaceRoot", bson.D{{"newRoot", "$_snapshot"}}}},
	}
)
