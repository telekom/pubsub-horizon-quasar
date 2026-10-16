package mongo

import (
	"context"
	"sync"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.mongodb.org/mongo-driver/mongo/writeconcern"
)

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

func (c *Client) openStream(resumeToken bson.Raw) (*mongo.ChangeStream, error) {
	streamOpts := options.ChangeStream()
	if len(resumeToken) > 0 {
		streamOpts.SetResumeAfter(resumeToken)
	}

	stream, err := c.mongoClient.Watch(c.clientCtx, mongo.Pipeline{}, streamOpts)
	if err != nil {
		return nil, err
	}

	return stream, nil
}

func (c *Client) readStream() {
	for c.stream.Next(c.clientCtx) {
		select {

		case c.streamChan <- c.stream.Current:
			// Just pass the current event

		case <-c.clientCtx.Done():
			return

		}
	}
}

func (c *Client) Close() {
	c.clientCancel()
	c.streamWg.Wait()
	close(c.streamChan)
}
