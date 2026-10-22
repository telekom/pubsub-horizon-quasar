// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package zoo

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Shopify/zk"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const autoIncrementVersion = -1

type Client struct {
	conn *zk.Conn
}

func NewClient(addresses []string, sessionTimeout time.Duration) (*Client, error) {
	zookeeperLog := log.With().Str("logger", "zoo").Logger()

	conn, _, err := zk.Connect(addresses, sessionTimeout, zk.WithLogger(slog.New(zerolog.NewSlogHandler(zookeeperLog))))
	if err != nil {
		return nil, err
	}

	return &Client{conn}, nil
}

func (c *Client) Exists(zNode string) (bool, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ok, _, err := c.conn.Exists(ctx, zNode)
	if err != nil && !errors.Is(err, zk.ErrNoNode) {
		return false, err
	}

	return ok, nil
}

func (c *Client) Read(zNode string) ([]byte, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bytes, _, err := c.conn.Get(ctx, zNode)
	return bytes, err
}

func (c *Client) Write(zNode string, data []byte) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := c.conn.Set(ctx, zNode, data, autoIncrementVersion)
	if errors.Is(err, zk.ErrNoNode) {
		return c.create(ctx, zNode, data)
	}

	return err
}

func (c *Client) create(ctx context.Context, zNode string, data []byte) error {
	nodes := strings.Split(zNode[1:], "/")

	nextNode := strings.Builder{}
	for i, node := range nodes {
		_, _ = nextNode.WriteString("/" + node)

		if isParentNode := i < len(nodes)-1; isParentNode {
			if _, err := c.conn.Create(ctx, nextNode.String(), make([]byte, 0), 0, zk.WorldACL(zk.PermAll)); err != nil {
				if errors.Is(err, zk.ErrNodeExists) {
					continue
				}
				return err
			}
		} else {
			_, err := c.conn.Create(ctx, nextNode.String(), data, 0, zk.WorldACL(zk.PermAll))
			return err
		}
	}

	return nil
}

func (c *Client) Disconnect() {
	c.conn.Close()
}
