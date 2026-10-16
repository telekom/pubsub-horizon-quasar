// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package zoo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Shopify/zk"
)

const autoIncrementVersion = -1

type Client struct {
	conn *zk.Conn
}

func NewClient(addresses []string, sessionTimeout time.Duration) (*Client, error) {
	conn, _, err := zk.Connect(addresses, sessionTimeout)
	if err != nil {
		return nil, err
	}
	return &Client{conn}, nil
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
		nextNode.WriteString("/" + node)

		if isParentNode := i < len(nodes)-1; isParentNode {
			if _, err := c.conn.Create(ctx, nextNode.String(), []byte{}, 0, zk.WorldACL(zk.PermAll)); err != nil {
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
