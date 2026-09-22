// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"slices"
	"time"

	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const (
	maxDocumentBytes = 16 * 1024 * 1024
	writeBatchBytes  = 1024 * 1024
	batchSize        = 500
)

type descriptor struct {
	SnapshotID    string    `bson:"snapshotId"`
	SourceHash    string    `bson:"sourceHash"`
	DocumentCount int64     `bson:"documentCount"`
	CreatedAt     time.Time `bson:"createdAt"`
}

type head struct {
	ID              string       `bson:"_id"`
	Version         descriptor   `bson:",inline"`
	RecentSnapshots []descriptor `bson:"recentSnapshots"`
}

type snapshotDocument struct {
	ID             primitive.ObjectID `bson:"_id"`
	SnapshotID     string             `bson:"snapshotId"`
	SubscriptionID string             `bson:"subscriptionId"`
	Resource       bson.Raw           `bson:"resource"`
}

type sourceBuffer struct {
	documents []bson.Raw
	bytes     int64
	digest    hash.Hash
	lastID    string
}

func newSourceBuffer() *sourceBuffer {
	return &sourceBuffer{digest: sha256.New()}
}

func (b *sourceBuffer) add(raw bson.Raw, limit int64) error {
	if err := raw.Validate(); err != nil {
		return errors.New("source contains invalid BSON")
	}
	id, ok := raw.Lookup("_id").StringValueOK()
	if !ok {
		return errors.New("source subscription _id must be a BSON string")
	}
	if len(b.documents) > 0 && id <= b.lastID {
		return errors.New("source IDs must be unique and sorted using simple collation")
	}
	if int64(len(raw)) > limit-b.bytes {
		return errors.New("source BSON payload exceeds subscriptionSnapshots.maxSnapshotBytes")
	}
	owned := slices.Clone(raw)
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(owned)))
	_, _ = b.digest.Write(length[:])
	_, _ = b.digest.Write(owned)
	b.documents = append(b.documents, owned)
	b.bytes += int64(len(owned))
	b.lastID = id
	return nil
}

func (b *sourceBuffer) sourceHash() string {
	return hex.EncodeToString(b.digest.Sum(nil))
}

func wrapDocument(raw bson.Raw, snapshotID string) (bson.Raw, error) {
	elements, err := raw.Elements()
	if err != nil {
		return nil, errors.New("source contains invalid BSON elements")
	}
	resource := make(bson.Raw, 4, len(raw))
	var id string
	var idCount int
	for _, element := range elements {
		if element.Key() == "_id" {
			var ok bool
			id, ok = element.Value().StringValueOK()
			if !ok {
				return nil, errors.New("source subscription _id must be a BSON string")
			}
			idCount++
			continue
		}
		resource = append(resource, element...)
	}
	if idCount != 1 {
		return nil, errors.New("source must contain exactly one subscription _id")
	}
	resource = append(resource, 0)
	binary.LittleEndian.PutUint32(resource[:4], uint32(len(resource)))
	result, err := bson.Marshal(snapshotDocument{
		ID: primitive.NewObjectID(), SnapshotID: snapshotID, SubscriptionID: id, Resource: resource,
	})
	if err != nil {
		return nil, errors.New("cannot encode snapshot resource")
	}
	if len(result) > maxDocumentBytes {
		return nil, errors.New("wrapped snapshot resource exceeds MongoDB's BSON document limit")
	}
	return result, nil
}

func decodeHead(raw bson.Raw) (head, error) {
	var result head
	if raw.Lookup("_id").Type != bson.TypeString || raw.Lookup("_id").StringValue() != "head" {
		return result, errors.New("head must have the string _id head")
	}
	history, ok := raw.Lookup("recentSnapshots").ArrayOK()
	if !ok {
		return result, errors.New("head recentSnapshots must be a BSON array")
	}
	values, err := history.Values()
	if err != nil || len(values) > config.MaxSnapshotHistory || len(raw) > maxDocumentBytes {
		return result, errors.New("head history is invalid or exceeds its size limit")
	}
	result.ID = "head"
	result.RecentSnapshots = make([]descriptor, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		item, err := decodeDescriptor(value)
		if err != nil {
			return head{}, err
		}
		if seen[item.SnapshotID] {
			return head{}, errors.New("head history contains duplicate snapshot IDs")
		}
		seen[item.SnapshotID] = true
		result.RecentSnapshots = append(result.RecentSnapshots, item)
	}
	if raw.Lookup("snapshotId").Type == 0 {
		if len(values) != 0 || raw.Lookup("sourceHash").Type != 0 ||
			raw.Lookup("documentCount").Type != 0 || raw.Lookup("createdAt").Type != 0 {

			return head{}, errors.New("bootstrap head must not contain active metadata or history")
		}
		return result, nil
	}
	active, err := decodeDescriptor(bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: raw})
	if err != nil {
		return head{}, err
	}
	if len(values) == 0 || !sameDescriptor(active, result.RecentSnapshots[0]) {
		return head{}, errors.New("head metadata must match its first history descriptor")
	}
	result.Version = active
	return result, nil
}

func decodeDescriptor(value bson.RawValue) (descriptor, error) {
	raw, ok := value.DocumentOK()
	if !ok || raw.Lookup("snapshotId").Type != bson.TypeString || raw.Lookup("sourceHash").Type != bson.TypeString ||
		raw.Lookup("documentCount").Type != bson.TypeInt64 || raw.Lookup("createdAt").Type != bson.TypeDateTime {

		return descriptor{}, errors.New("snapshot descriptor has missing fields or invalid BSON types")
	}
	var result descriptor
	if err := bson.Unmarshal(raw, &result); err != nil {
		return result, errors.New("cannot decode snapshot descriptor")
	}
	id, err := canonicalID(result.SnapshotID)
	if err != nil {
		return result, err
	}
	digest, err := hex.DecodeString(result.SourceHash)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != result.SourceHash ||
		result.DocumentCount < 0 || !result.CreatedAt.Equal(id.Timestamp()) {

		return result, errors.New("snapshot descriptor has an invalid hash, count or creation time")
	}
	return result, nil
}

func canonicalID(value string) (primitive.ObjectID, error) {
	id, err := primitive.ObjectIDFromHex(value)
	if err != nil || id.Hex() != value {
		return primitive.NilObjectID, errors.New("snapshotId must be a canonical lowercase ObjectID hex string")
	}
	return id, nil
}

func sameDescriptor(a, b descriptor) bool {
	return a.SnapshotID == b.SnapshotID && a.SourceHash == b.SourceHash &&
		a.DocumentCount == b.DocumentCount && a.CreatedAt.Equal(b.CreatedAt)
}

func sameHead(a, b head) bool {
	return a.ID == b.ID && sameDescriptor(a.Version, b.Version) &&
		slices.EqualFunc(a.RecentSnapshots, b.RecentSnapshots, sameDescriptor)
}

func proposeHead(previous head, next descriptor, retained int) head {
	history := make([]descriptor, 0, min(retained, len(previous.RecentSnapshots)+1))
	history = append(history, next)
	history = append(history, previous.RecentSnapshots[:min(len(previous.RecentSnapshots), retained-1)]...)
	return head{ID: "head", Version: next, RecentSnapshots: history}
}
