// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"sync"
	"time"

	"github.com/Shopify/zk"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
)

const zooKeeperIOTimeout = 5 * time.Second

var errZooKeeperUnavailable = errors.New("no usable ZooKeeper session")

type zNode struct {
	exists  bool
	version int32
	czxid   int64
	session int64
	value   descriptor
}

func sameNode(a, b zNode) bool {
	return a.exists == b.exists && (!a.exists ||
		a.version == b.version && a.czxid == b.czxid && sameDescriptor(a.value, b.value))
}

type writeOutcome uint8

const (
	writeNotExecuted writeOutcome = iota
	writeConfirmed
	writeUncertain
)

type zooKeeperTransport interface {
	available() bool
	events() <-chan struct{}
	read(context.Context, string) (zNode, error)
	write(context.Context, string, descriptor, zNode) (zNode, writeOutcome, error)
	close(context.Context) error
}

type zooKeeperClient struct {
	config config.SnapshotZooKeeper
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	mu     sync.RWMutex
	conn   *zk.Conn
}

func newZooKeeperClient(ctx context.Context, c config.SnapshotZooKeeper, retry time.Duration) *zooKeeperClient {
	ctx, cancel := context.WithCancel(ctx)
	client := &zooKeeperClient{
		config: c, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	go client.run(ctx, retry)
	return client
}

func (c *zooKeeperClient) run(ctx context.Context, retry time.Duration) {
	defer close(c.done)
	for ctx.Err() == nil {
		conn, events, err := zk.Connect(c.config.Addresses, c.config.SessionTimeout,
			zk.WithLogger(slog.New(&zooKeeperLogHandler{logger: log.Logger})))
		if err != nil {
			log.Error().Err(zooKeeperError("connect", err)).Msg("Subscription snapshot ZooKeeper initialization failed")
			if !waitZooKeeperRetry(ctx, retry) {
				return
			}
			continue
		}
		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()
		c.consumeEvents(ctx, conn, events)
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		if ctx.Err() == nil && !waitZooKeeperRetry(ctx, retry) {
			return
		}
	}
}

func waitZooKeeperRetry(ctx context.Context, retry time.Duration) bool {
	timer := time.NewTimer(retry)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *zooKeeperClient) consumeEvents(ctx context.Context, conn *zk.Conn, events <-chan zk.Event) {
	for {
		select {
		case <-ctx.Done():
			conn.Close()
			for range events {
			}
			return
		case _, ok := <-events:
			select {
			case c.wake <- struct{}{}:
			default:
			}
			if !ok {
				conn.Close()
				return
			}
		}
	}
}

func (c *zooKeeperClient) connection() (*zk.Conn, int64, error) {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil || conn.State() != zk.StateHasSession {
		return nil, 0, zooKeeperError("session", zk.ErrConnectionClosed)
	}
	return conn, conn.SessionID(), nil
}

func (c *zooKeeperClient) available() bool {
	_, _, err := c.connection()
	return err == nil
}

func (c *zooKeeperClient) events() <-chan struct{} {
	return c.wake
}

func (c *zooKeeperClient) close(ctx context.Context) error {
	c.cancel()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return zooKeeperError("close", ctx.Err())
	}
}

func checkZooKeeperSession(conn *zk.Conn, session int64) error {
	if conn.State() != zk.StateHasSession || conn.SessionID() != session {
		return zooKeeperError("session changed", zk.ErrSessionExpired)
	}
	return nil
}

func (c *zooKeeperClient) read(ctx context.Context, name string) (zNode, error) {
	conn, session, err := c.connection()
	if err != nil {
		return zNode{}, err
	}
	return c.readOn(ctx, conn, session, path.Join(c.config.BasePath, name))
}

func (c *zooKeeperClient) readOn(ctx context.Context, conn *zk.Conn, session int64, nodePath string) (zNode, error) {
	syncPath := nodePath
	for {
		exists, _, err := conn.Exists(ctx, syncPath)
		if err != nil {
			return zNode{}, zooKeeperError("find sync parent", err)
		}
		if exists {
			break
		}
		if syncPath == "/" {
			return zNode{}, integrityError("ZooKeeper root is missing")
		}
		syncPath = path.Dir(syncPath)
	}
	if _, err := conn.Sync(ctx, syncPath); err != nil {
		return zNode{}, zooKeeperError("sync", err)
	}
	if err := checkZooKeeperSession(conn, session); err != nil {
		return zNode{}, err
	}
	data, stat, err := conn.Get(ctx, nodePath)
	if sessionErr := checkZooKeeperSession(conn, session); sessionErr != nil {
		return zNode{}, sessionErr
	}
	if errors.Is(err, zk.ErrNoNode) {
		return zNode{session: session}, nil
	}
	if err != nil {
		return zNode{}, zooKeeperError("get", err)
	}
	if stat.EphemeralOwner != 0 {
		return zNode{}, integrityError("ZooKeeper snapshot nodes must be persistent")
	}
	value, err := decodeJSONDescriptor(data)
	if err != nil {
		return zNode{}, integrityError(err.Error())
	}
	return zNode{exists: true, version: stat.Version, czxid: stat.Czxid, session: session, value: value}, nil
}

func (c *zooKeeperClient) ensureParents(ctx context.Context, conn *zk.Conn, session int64, nodePath string) error {
	parent := path.Dir(nodePath)
	if parent == "/" {
		return nil
	}
	exists, stat, err := conn.Exists(ctx, parent)
	if err != nil {
		return zooKeeperError("check parent", err)
	}
	if exists {
		if stat.EphemeralOwner != 0 {
			return integrityError("ZooKeeper snapshot parents must be persistent")
		}
		return checkZooKeeperSession(conn, session)
	}
	if err := c.ensureParents(ctx, conn, session, parent); err != nil {
		return err
	}
	if err := checkZooKeeperSession(conn, session); err != nil {
		return err
	}
	_, err = conn.Create(ctx, parent, nil, 0, zk.WorldACL(zk.PermAll))
	if err != nil && !errors.Is(err, zk.ErrNodeExists) {
		return zooKeeperError("create parent", err)
	}
	_, stat, err = conn.Get(ctx, parent)
	if err != nil {
		return zooKeeperError("check parent", err)
	}
	if stat.EphemeralOwner != 0 {
		return integrityError("ZooKeeper snapshot parents must be persistent")
	}
	return checkZooKeeperSession(conn, session)
}

func (c *zooKeeperClient) write(ctx context.Context, name string, value descriptor, expected zNode) (zNode, writeOutcome, error) {
	data, err := encodeDescriptor(value)
	if err != nil {
		return zNode{}, writeNotExecuted, integrityError(err.Error())
	}
	conn, session, err := c.connection()
	if err != nil {
		return zNode{}, writeNotExecuted, err
	}
	if expected.session != session {
		return zNode{}, writeNotExecuted, zooKeeperError("write session changed", zk.ErrSessionExpired)
	}
	nodePath := path.Join(c.config.BasePath, name)
	if !expected.exists {
		if err := c.ensureParents(ctx, conn, session, nodePath); err != nil {
			return zNode{}, writeNotExecuted, err
		}
	}
	if err := ctx.Err(); err != nil {
		return zNode{}, writeNotExecuted, zooKeeperError("write", err)
	}
	if err := checkZooKeeperSession(conn, session); err != nil {
		return zNode{}, writeNotExecuted, err
	}
	if expected.exists {
		stat, err := conn.Set(ctx, nodePath, data, expected.version)
		if err != nil {
			return zNode{}, classifyWriteOutcome(err), zooKeeperError("set", err)
		}
		if err := checkZooKeeperSession(conn, session); err != nil {
			return zNode{}, writeUncertain, err
		}
		if stat.Czxid != expected.czxid || stat.Version != expected.version+1 || stat.EphemeralOwner != 0 {
			return zNode{}, writeUncertain, integrityError("ZooKeeper node identity or version changed during set")
		}
		return zNode{exists: true, version: stat.Version, czxid: stat.Czxid, session: session, value: value}, writeConfirmed, nil
	}
	_, err = conn.Create(ctx, nodePath, data, 0, zk.WorldACL(zk.PermAll))
	if err != nil {
		return zNode{}, classifyWriteOutcome(err), zooKeeperError("create", err)
	}
	current, err := c.readOn(ctx, conn, session, nodePath)
	if err != nil {
		return zNode{}, writeUncertain, err
	}
	if !current.exists || current.version != 0 || !sameDescriptor(current.value, value) {
		return zNode{}, writeUncertain, integrityError("ZooKeeper created node has unexpected data or version")
	}
	return current, writeConfirmed, nil
}

func classifyWriteOutcome(err error) writeOutcome {
	for _, rejection := range []error{
		zk.ErrNoNode, zk.ErrNodeExists, zk.ErrBadVersion, zk.ErrNoAuth, zk.ErrAuthFailed,
		zk.ErrInvalidACL, zk.ErrInvalidFlags, zk.ErrBadArguments, zk.ErrNoChildrenForEphemerals,
	} {
		if errors.Is(err, rejection) {
			return writeNotExecuted
		}
	}
	return writeUncertain
}

type zooKeeperOperationError struct {
	operation string
	category  string
	cause     error
}

func (e *zooKeeperOperationError) Error() string {
	if e.category == "invalid" {
		return "ZooKeeper integrity failed: " + e.cause.Error()
	}
	return "ZooKeeper " + e.operation + " failed (" + e.category + ")"
}

func (e *zooKeeperOperationError) Unwrap() error {
	return e.cause
}

func zooKeeperError(operation string, err error) error {
	return &zooKeeperOperationError{operation: operation, category: zooKeeperErrorCategory(err), cause: err}
}

func zooKeeperErrorCategory(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, zk.ErrNoNode):
		return "missing"
	case errors.Is(err, zk.ErrNodeExists):
		return "exists"
	case errors.Is(err, zk.ErrBadVersion):
		return "version_conflict"
	case errors.Is(err, zk.ErrNoAuth), errors.Is(err, zk.ErrAuthFailed), errors.Is(err, zk.ErrInvalidACL):
		return "access"
	case errors.Is(err, errZooKeeperUnavailable), errors.Is(err, zk.ErrConnectionClosed), errors.Is(err, zk.ErrClosing),
		errors.Is(err, zk.ErrSessionExpired), errors.Is(err, zk.ErrSessionMoved):
		return "unavailable"
	default:
		return "other"
	}
}

func integrityError(message string) error {
	return &zooKeeperOperationError{operation: "integrity", category: "invalid", cause: errors.New(message)}
}

type zooKeeperLogHandler struct {
	logger zerolog.Logger
	attrs  []slog.Attr
}

func (h *zooKeeperLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.GetLevel() <= zooKeeperLogLevel(level)
}

func zooKeeperLogLevel(level slog.Level) zerolog.Level {
	switch {
	case level >= slog.LevelError:
		return zerolog.ErrorLevel
	case level >= slog.LevelWarn:
		return zerolog.WarnLevel
	case level >= slog.LevelInfo:
		return zerolog.InfoLevel
	default:
		return zerolog.DebugLevel
	}
}

func (h *zooKeeperLogHandler) Handle(_ context.Context, record slog.Record) error {
	var category string
	// SDK records may contain wire payloads; forward only error categories and severity.
	addCategory := func(attr slog.Attr) {
		if err, ok := attr.Value.Any().(error); ok {
			category = zooKeeperErrorCategory(err)
		}
	}
	for _, attr := range h.attrs {
		addCategory(attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addCategory(attr)
		return true
	})
	h.logger.WithLevel(zooKeeperLogLevel(record.Level)).Str("source", "zookeeper-sdk").
		Str("errorCategory", category).Msg("ZooKeeper SDK transport event")
	return nil
}

func (h *zooKeeperLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copyHandler := *h
	copyHandler.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &copyHandler
}

func (h *zooKeeperLogHandler) WithGroup(_ string) slog.Handler {
	return h
}
