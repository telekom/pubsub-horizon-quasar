// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

type mongoStore struct {
	database  *mongo.Database
	source    *mongo.Collection
	snapshots *mongo.Collection
	heads     *mongo.Collection
	seenHead  bool
}

func newMongoStore(client *mongo.Client, c config.SubscriptionSnapshots) *mongoStore {
	journal := true
	wc := &writeconcern.WriteConcern{W: "majority", Journal: &journal, WTimeout: c.OperationTimeout}
	database := client.Database(c.Database, options.Database().
		SetReadPreference(readpref.Primary()).SetReadConcern(readconcern.Majority()).SetWriteConcern(wc))
	return &mongoStore{
		database: database, source: database.Collection(c.SourceCollection),
		snapshots: database.Collection(c.SnapshotCollection), heads: database.Collection(c.HeadCollection),
	}
}

func (m *mongoStore) readHead(ctx context.Context) (head, error) {
	raw, err := m.heads.FindOne(ctx, bson.D{{Key: "_id", Value: "head"}}).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return m.bootstrap(ctx)
	}
	if err != nil {
		return head{}, databaseError("read head", err)
	}
	current, err := decodeHead(raw)
	if err == nil {
		m.seenHead = true
	}
	return current, err
}

func (m *mongoStore) bootstrap(ctx context.Context) (head, error) {
	if m.seenHead {
		return head{}, errors.New("previously observed head is missing; restore publication metadata")
	}
	for _, collection := range []*mongo.Collection{m.snapshots, m.heads} {
		count, err := collection.CountDocuments(ctx, bson.D{}, options.Count().SetLimit(1))
		if err != nil {
			return head{}, databaseError("check bootstrap state", err)
		}
		if count != 0 {
			return head{}, errors.New("head missing alongside existing snapshot/head data; restore publication metadata")
		}
	}
	_, err := m.heads.InsertOne(ctx, bson.D{
		{Key: "_id", Value: "head"}, {Key: "recentSnapshots", Value: bson.A{}},
	})
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return head{}, databaseError("bootstrap head", err)
	}
	raw, err := m.heads.FindOne(ctx, bson.D{{Key: "_id", Value: "head"}}).Raw()
	if err != nil {
		return head{}, databaseError("confirm bootstrap head", err)
	}
	current, err := decodeHead(raw)
	if err == nil {
		m.seenHead = true
	}
	return current, err
}

func (m *mongoStore) readSource(ctx context.Context, limit int64) (*sourceBuffer, error) {
	if err := m.requireSourceCollection(ctx); err != nil {
		return nil, err
	}
	cursor, err := m.source.Find(ctx, bson.D{}, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).SetCollation(&options.Collation{Locale: "simple"}).
		SetBatchSize(batchSize))
	if err != nil {
		return nil, databaseError("read source", err)
	}
	defer closeCursor(ctx, cursor)
	buffer := newSourceBuffer()
	for cursor.Next(ctx) {
		if err := buffer.add(cursor.Current, limit); err != nil {
			return nil, err
		}
	}
	if err := cursor.Err(); err != nil {
		return nil, databaseError("finish source scan", err)
	}
	return buffer, nil
}

func (m *mongoStore) insertSnapshot(ctx context.Context, id string, source *sourceBuffer) error {
	documents := make([]any, 0, batchSize)
	var size int
	flush := func() error {
		if len(documents) == 0 {
			return nil
		}
		_, err := m.snapshots.InsertMany(ctx, documents, options.InsertMany().SetOrdered(true))
		clear(documents)
		documents = documents[:0]
		size = 0
		return databaseError("insert snapshot batch", err)
	}
	for _, raw := range source.documents {
		document, err := wrapDocument(raw, id)
		if err != nil {
			return err
		}
		if len(documents) == batchSize || size+len(document) > writeBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		documents = append(documents, document)
		size += len(document)
	}
	return flush()
}

func (m *mongoStore) activate(ctx context.Context, previous string, next head) (bool, error) {
	var expected any = previous
	if previous == "" {
		expected = bson.D{{Key: "$exists", Value: false}}
	}
	result, err := m.heads.ReplaceOne(ctx, bson.D{
		{Key: "_id", Value: "head"}, {Key: "snapshotId", Value: expected},
	}, next, options.Replace().SetUpsert(false))
	if err != nil {
		return false, databaseError("activate head", err)
	}
	return result.MatchedCount == 1, nil
}

func (m *mongoStore) countSnapshot(ctx context.Context, id string) (int64, error) {
	count, err := m.snapshots.CountDocuments(ctx, bson.D{{Key: "snapshotId", Value: id}})
	return count, databaseError("count snapshot", err)
}

func (m *mongoStore) visitSnapshotIDs(ctx context.Context, visit func(string) error) error {
	cursor, err := m.snapshots.Aggregate(ctx, mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$snapshotId"}}}},
	}, options.Aggregate().SetAllowDiskUse(true).SetBatchSize(batchSize))
	if err != nil {
		return databaseError("list snapshot versions", err)
	}
	defer closeCursor(ctx, cursor)
	for cursor.Next(ctx) {
		id, ok := cursor.Current.Lookup("_id").StringValueOK()
		if !ok {
			return errors.New("snapshot collection contains a non-string snapshotId; cleanup aborted")
		}
		if err := visit(id); err != nil {
			return err
		}
	}
	return databaseError("finish listing snapshot versions", cursor.Err())
}

func (m *mongoStore) deleteBatch(ctx context.Context, id string) (int64, error) {
	cursor, err := m.snapshots.Find(ctx, bson.D{{Key: "snapshotId", Value: id}},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}).SetLimit(batchSize))
	if err != nil {
		return 0, databaseError("select snapshot deletion batch", err)
	}
	defer closeCursor(ctx, cursor)
	ids := make(bson.A, 0, batchSize)
	for cursor.Next(ctx) {
		id := cursor.Current.Lookup("_id")
		id.Value = slices.Clone(id.Value)
		ids = append(ids, id)
	}
	if err := cursor.Err(); err != nil {
		return 0, databaseError("finish snapshot deletion selection", err)
	}
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := m.snapshots.DeleteMany(ctx, bson.D{
		{Key: "snapshotId", Value: id}, {Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}},
	})
	if err != nil {
		return 0, databaseError("delete snapshot batch", err)
	}
	return result.DeletedCount, nil
}

func closeCursor(ctx context.Context, cursor *mongo.Cursor) {
	if err := cursor.Close(ctx); err != nil {
		log.Error().Err(databaseError("close cursor", err)).Msg("Subscription snapshot cursor cleanup failed")
	}
}

type mongoOperationError struct {
	operation string
	cause     error
}

func (e *mongoOperationError) Error() string {
	message := e.operation + " failed"
	var commandError mongo.CommandError
	if errors.As(e.cause, &commandError) {
		message += " (MongoDB code " + strconv.Itoa(int(commandError.Code)) + ")"
	}
	if mongo.IsTimeout(e.cause) {
		message += " (timeout)"
	}
	return message
}

func (e *mongoOperationError) Unwrap() error {
	return e.cause
}

func databaseError(operation string, err error) error {
	if err == nil {
		return nil
	}
	// Server/driver messages can contain URIs, credentials or rejected subscription documents.
	return &mongoOperationError{operation: operation, cause: err}
}
