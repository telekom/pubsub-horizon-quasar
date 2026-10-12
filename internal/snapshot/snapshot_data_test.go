// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

//go:build testing

package snapshot

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestSourceBuffer checks owned BSON copies, stable hashes and rejection of invalid IDs or payload overflow.
func TestSourceBuffer(t *testing.T) {
	raw := sourceDocument(t, "a", "first")
	original := append(bson.Raw(nil), raw...)
	buffer := newSourceBuffer()
	require.NoError(t, buffer.add(raw, int64(len(raw))))
	clear(raw)
	require.Equal(t, original, buffer.documents[0], "cursor buffer must be owned")
	expected := newSourceBuffer()
	require.NoError(t, expected.add(original, int64(len(original))))
	require.Equal(t, expected.sourceHash(), buffer.sourceHash())
	tests := []struct {
		name  string
		raw   bson.Raw
		limit int64
	}{
		{"overflow", sourceDocument(t, "b", "next"), int64(len(original))},
		{"duplicate", original, 1024},
		{"wrong ID type", marshal(t, bson.D{{Key: "_id", Value: primitive.NewObjectID()}}), 1024},
		{"missing ID", marshal(t, bson.D{{Key: "spec", Value: bson.D{}}}), 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { require.Error(t, buffer.add(tt.raw, tt.limit)) })
	}
	changed := newSourceBuffer()
	require.NoError(t, changed.add(sourceDocument(t, "a", "updated"), 1024))
	require.NotEqual(t, buffer.sourceHash(), changed.sourceHash())
}

// TestSourceHashIgnoresBSONFieldOrder checks canonical hashing without changing the original stored BSON.
func TestSourceHashIgnoresBSONFieldOrder(t *testing.T) {
	date := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	original := marshal(t, bson.D{
		{Key: "_id", Value: "a"},
		{Key: "metadata", Value: bson.D{
			{Key: "annotations", Value: bson.D{{Key: "z", Value: "last"}, {Key: "a", Value: "first"}}},
			{Key: "labels", Value: bson.D{{Key: "b", Value: true}, {Key: "a", Value: false}}},
		}},
		{Key: "spec", Value: bson.D{
			{Key: "subscription", Value: bson.D{
				{Key: "enabled", Value: true},
				{Key: "date", Value: date},
				{Key: "binary", Value: primitive.Binary{Data: []byte{1, 2}}},
			}},
			{Key: "filters", Value: bson.A{bson.D{{Key: "z", Value: int32(1)}, {Key: "a", Value: int64(2)}}}},
		}},
	})
	reordered := marshal(t, bson.D{
		{Key: "spec", Value: bson.D{
			{Key: "filters", Value: bson.A{bson.D{{Key: "a", Value: int64(2)}, {Key: "z", Value: int32(1)}}}},
			{Key: "subscription", Value: bson.D{
				{Key: "binary", Value: primitive.Binary{Data: []byte{1, 2}}},
				{Key: "date", Value: date},
				{Key: "enabled", Value: true},
			}},
		}},
		{Key: "metadata", Value: bson.D{
			{Key: "labels", Value: bson.D{{Key: "a", Value: false}, {Key: "b", Value: true}}},
			{Key: "annotations", Value: bson.D{{Key: "a", Value: "first"}, {Key: "z", Value: "last"}}},
		}},
		{Key: "_id", Value: "a"},
	})
	require.NotEqual(t, original, reordered)
	first, second := newSourceBuffer(), newSourceBuffer()
	require.NoError(t, first.add(original, int64(len(original))))
	require.NoError(t, second.add(reordered, int64(len(reordered))))
	require.Equal(t, first.sourceHash(), second.sourceHash())
	require.Equal(t, original, first.documents[0])
	require.Equal(t, reordered, second.documents[0])

	wrapped, err := wrapDocument(second.documents[0], primitive.NewObjectID().Hex())
	require.NoError(t, err)
	require.Equal(t, reordered.Lookup("spec").Document(), wrapped.Lookup("resource", "spec").Document())
}

// TestSourceHashDetectsBSONContentChanges checks that values, BSON types and array order affect the hash.
func TestSourceHashDetectsBSONContentChanges(t *testing.T) {
	original := marshal(t, bson.D{
		{Key: "_id", Value: "a"},
		{Key: "spec", Value: bson.D{
			{Key: "value", Value: int32(1)},
			{Key: "items", Value: bson.A{int32(1), int32(2)}},
		}},
	})
	base := newSourceBuffer()
	require.NoError(t, base.add(original, int64(len(original))))
	tests := []struct {
		name string
		raw  bson.Raw
	}{
		{"value", marshal(t, bson.D{
			{Key: "_id", Value: "a"},
			{Key: "spec", Value: bson.D{{Key: "value", Value: int32(2)}, {Key: "items", Value: bson.A{int32(1), int32(2)}}}},
		})},
		{"type", marshal(t, bson.D{
			{Key: "_id", Value: "a"},
			{Key: "spec", Value: bson.D{{Key: "value", Value: int64(1)}, {Key: "items", Value: bson.A{int32(1), int32(2)}}}},
		})},
		{"array order", marshal(t, bson.D{
			{Key: "_id", Value: "a"},
			{Key: "spec", Value: bson.D{{Key: "value", Value: int32(1)}, {Key: "items", Value: bson.A{int32(2), int32(1)}}}},
		})},
		{"field name", marshal(t, bson.D{
			{Key: "_id", Value: "a"},
			{Key: "spec", Value: bson.D{{Key: "other", Value: int32(1)}, {Key: "items", Value: bson.A{int32(1), int32(2)}}}},
		})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := newSourceBuffer()
			require.NoError(t, changed.add(tt.raw, int64(len(tt.raw))))
			require.NotEqual(t, base.sourceHash(), changed.sourceHash())
		})
	}
}

// TestSnapshotMappingPreservesBSON checks ID remapping, unchanged nested BSON and duplicate source-ID rejection.
func TestSnapshotMappingPreservesBSON(t *testing.T) {
	id := primitive.NewObjectID()
	resource := bson.D{
		{Key: "spec", Value: bson.D{
			{Key: "count", Value: int64(1)},
			{Key: "date", Value: id.Timestamp()},
			{Key: "binary", Value: primitive.Binary{Subtype: 0, Data: []byte{1, 2}}},
			{Key: "objectId", Value: id},
			{Key: "nested", Value: bson.D{{Key: "_id", Value: "keep"}}},
		}},
	}
	source := append(bson.D{{Key: "_id", Value: "subscription"}}, resource...)
	raw, err := wrapDocument(marshal(t, source), id.Hex())
	require.NoError(t, err)
	require.Equal(t, bson.TypeObjectID, raw.Lookup("_id").Type)
	require.Equal(t, bson.TypeString, raw.Lookup("snapshotId").Type)
	require.Equal(t, id.Hex(), raw.Lookup("snapshotId").StringValue())
	require.Equal(t, "subscription", raw.Lookup("subscriptionId").StringValue())
	require.Equal(t, marshal(t, resource), raw.Lookup("resource").Document())
	require.Equal(t, bson.TypeInt64, raw.Lookup("resource", "spec", "count").Type)
	require.Equal(t, "keep", raw.Lookup("resource", "spec", "nested", "_id").StringValue())
	_, err = wrapDocument(marshal(t, bson.D{{Key: "_id", Value: "a"}, {Key: "_id", Value: "b"}}), id.Hex())
	require.Error(t, err)
}

// TestHeadBSONContract checks exact metadata BSON types and round-trips active and bootstrap heads.
func TestHeadBSONContract(t *testing.T) {
	for _, count := range []int64{0, 1, 123} {
		version := testDescriptor(time.Now(), count)
		current := proposeHead(head{}, version, 3)
		raw := marshal(t, current)
		require.Equal(t, bson.TypeInt64, raw.Lookup("documentCount").Type)
		require.Equal(t, bson.TypeString, raw.Lookup("snapshotId").Type)
		require.Equal(t, bson.TypeInt64, raw.Lookup("recentSnapshots", "0", "documentCount").Type)
		decoded, err := decodeHead(raw)
		require.NoError(t, err)
		require.True(t, sameHead(current, decoded))
	}
	bootstrap, err := decodeHead(marshal(t, bson.D{
		{Key: "_id", Value: "head"}, {Key: "recentSnapshots", Value: bson.A{}},
	}))
	require.NoError(t, err)
	require.Empty(t, bootstrap.Version.SnapshotID)
}

// TestInvalidHeadMetadata checks that invalid descriptor fields and inconsistent history are rejected.
func TestInvalidHeadMetadata(t *testing.T) {
	version := testDescriptor(time.Now(), 1)
	tests := []struct {
		name   string
		change func(bson.M)
	}{
		{"native ObjectID", func(m bson.M) { m["snapshotId"] = primitive.NewObjectID() }},
		{"uppercase ID", func(m bson.M) { m["snapshotId"] = strings.ToUpper(version.SnapshotID) }},
		{"int32 count", func(m bson.M) { m["documentCount"] = int32(1) }},
		{"negative count", func(m bson.M) { m["documentCount"] = int64(-1) }},
		{"missing count", func(m bson.M) { delete(m, "documentCount") }},
		{"bad hash", func(m bson.M) { m["sourceHash"] = "invalid" }},
		{"wrong timestamp", func(m bson.M) { m["createdAt"] = time.Unix(0, 0) }},
		{"null snapshot", func(m bson.M) { m["snapshotId"] = nil }},
		{"empty snapshot", func(m bson.M) { m["snapshotId"] = "" }},
		{"missing history", func(m bson.M) { delete(m, "recentSnapshots") }},
		{"empty active history", func(m bson.M) { m["recentSnapshots"] = bson.A{} }},
		{"duplicate history", func(m bson.M) { m["recentSnapshots"] = bson.A{version, version} }},
		{"mismatched history", func(m bson.M) { m["recentSnapshots"] = bson.A{testDescriptor(time.Now(), 2)} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var document bson.M
			require.NoError(t, bson.Unmarshal(marshal(t, proposeHead(head{}, version, 3)), &document))
			tt.change(document)
			_, err := decodeHead(marshal(t, document))
			require.Error(t, err)
		})
	}
}

// TestHistoryUsesActivationOrder checks that retention follows publication order rather than creation timestamps.
func TestHistoryUsesActivationOrder(t *testing.T) {
	current := head{}
	times := []time.Time{time.Now(), time.Now().Add(-24 * time.Hour), time.Now().Add(-48 * time.Hour), time.Now()}
	var versions []descriptor
	for _, at := range times {
		version := testDescriptor(at, 0)
		versions = append(versions, version)
		current = proposeHead(current, version, 3)
	}
	require.Equal(t, []descriptor{versions[3], versions[2], versions[1]}, current.RecentSnapshots)
}

// TestBelowMinimumRetainsThreeSnapshots checks the minimum history capacity even for a smaller requested value.
func TestBelowMinimumRetainsThreeSnapshots(t *testing.T) {
	current := head{}
	var versions []descriptor
	for i := range 4 {
		version := testDescriptor(time.Now().Add(time.Duration(i)*time.Second), 0)
		versions = append(versions, version)
		current = proposeHead(current, version, 0)
	}
	require.Equal(t, []descriptor{versions[3], versions[2], versions[1]}, current.RecentSnapshots)
}
