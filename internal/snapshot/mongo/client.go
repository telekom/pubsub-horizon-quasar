// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package mongo

import (
	"context"
	"sync"

	zlog "github.com/rs/zerolog/log"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

var log = zlog.With().Str("pkg", "mongo").Logger()

type Client struct {
	mongoClient *mongo.Client

	clientCtx    context.Context
	clientCancel context.CancelFunc

	sourceCollection *mongo.Collection
	targetCollection *mongo.Collection

	stream     *mongo.ChangeStream
	streamChan chan bson.Raw
	streamWg   sync.WaitGroup
}

func NewClient(mongoUri string, databaseName string, sourceColName string, targetColName string) (*Client, error) {
	clientOpts := options.Client().
		SetReadPreference(readpref.Nearest()).
		SetWriteConcern(writeconcern.Majority()).
		ApplyURI(mongoUri)

	ctx, cancel := context.WithCancel(context.Background())
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		cancel()
		return nil, err
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		cancel()
		return nil, err
	}

	database := client.Database(databaseName)
	sourceCollection := database.Collection(sourceColName)
	targetCollection := database.Collection(targetColName)

	return &Client{
		mongoClient:      client,
		clientCtx:        ctx,
		clientCancel:     cancel,
		sourceCollection: sourceCollection,
		targetCollection: targetCollection,
		streamChan:       make(chan bson.Raw),
		streamWg:         sync.WaitGroup{},
	}, nil
}

func (c *Client) Connect(resumeToken bson.Raw) error {
	stream, err := c.openStream(resumeToken)
	if err != nil {
		return err
	}

	c.stream = stream
	c.streamWg.Go(c.readStream)

	return nil
}

func (c *Client) ResumeToken() bson.Raw {
	if c.stream == nil {
		return []byte{}
	}

	return c.stream.ResumeToken()
}

func (c *Client) GetUpdates() <-chan bson.Raw {
	return c.streamChan
}

func (c *Client) openStream(resumeToken bson.Raw) (*mongo.ChangeStream, error) {
	ctx, cancel := context.WithCancel(c.clientCtx)
	defer cancel()

	streamOpts := options.ChangeStream()
	if len(resumeToken) > 0 {
		streamOpts.SetResumeAfter(resumeToken)
	}

	stream, err := c.sourceCollection.Watch(ctx, mongo.Pipeline{}, streamOpts)
	if err != nil {
		if isResumeTokenErr(err) {
			log.Warn().
				Err(err).
				Msg("could not open change stream with existing resume token, opening channel without one")

			streamOpts.SetResumeAfter(nil)
			return c.sourceCollection.Watch(ctx, mongo.Pipeline{}, streamOpts)
		}

		return nil, err
	}

	return stream, nil
}

func (c *Client) readStream() {
	ctx, cancel := context.WithCancel(c.clientCtx)
	defer cancel()

	for c.stream.Next(ctx) {
		select {

		case c.streamChan <- c.stream.ResumeToken():
			// Just pass the current resume token

		case <-c.clientCtx.Done():
			return

		}
	}
}

func (c *Client) CreateSnapshot() (string, int64, error) {
	ctx, cancel := context.WithCancel(c.clientCtx)
	defer cancel()

	snapshotId := primitive.NewObjectID()

	cursor, err := c.sourceCollection.Aggregate(ctx, aggregateSnapshot)
	if err != nil {
		return "", 0, err
	}
	defer cursor.Close(ctx)

	bulk, err := c.readCursor(cursor, snapshotId)
	if err != nil {
		return "", 0, err
	}

	result, err := c.writeBulk(ctx, bulk)
	if err != nil {
		return "", 0, err
	}

	return snapshotId.Hex(), result.InsertedCount, nil
}

func (c *Client) readCursor(cursor *mongo.Cursor, snapshotId primitive.ObjectID) ([]mongo.WriteModel, error) {
	ctx, cancel := context.WithCancel(c.clientCtx)
	defer cancel()

	bulk := make([]mongo.WriteModel, 0)
	for cursor.Next(ctx) {
		var doc bson.M
		if err := cursor.Decode(&doc); err != nil {
			return bulk, err
		}

		doc["_id"] = primitive.NewObjectID()
		doc["snapshotId"] = snapshotId.Hex()

		bulk = append(bulk, &mongo.InsertOneModel{Document: doc})
	}

	return bulk, cursor.Err()
}

func (c *Client) writeBulk(ctx context.Context, bulk []mongo.WriteModel) (*mongo.BulkWriteResult, error) {
	session, err := c.mongoClient.StartSession()
	if err != nil {
		return nil, err
	}
	defer session.EndSession(context.Background())

	result, err := session.WithTransaction(ctx, func(sessionCtx mongo.SessionContext) (any, error) {
		return c.targetCollection.BulkWrite(sessionCtx, bulk)
	})

	if err != nil {
		return nil, err
	}

	return result.(*mongo.BulkWriteResult), nil
}

func (c *Client) DeleteStaleSnapshots(retainedSnapshotIds []string) (*mongo.DeleteResult, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	query := bson.D{{"snapshotId", bson.D{{"$nin", retainedSnapshotIds}}}}
	return c.targetCollection.DeleteMany(ctx, query)
}

func (c *Client) Close() {
	c.clientCancel()
	c.streamWg.Wait()
	close(c.streamChan)
}
