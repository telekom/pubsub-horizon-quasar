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

// sameNode compares persistent node identity, version and metadata, ignoring the observing session.
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

// newZooKeeperClient starts a cancellable connection loop with coalesced session-change notifications.
func newZooKeeperClient(ctx context.Context, c config.SnapshotZooKeeper, retry time.Duration) *zooKeeperClient {
	ctx, cancel := context.WithCancel(ctx)
	client := &zooKeeperClient{
		config: c, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	go client.run(ctx, retry)
	return client
}

// run reconnects ZooKeeper after closed event streams, spacing attempts until cancellation.
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

// waitZooKeeperRetry waits for the retry interval and returns false if shutdown happens first.
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

// consumeEvents coalesces SDK events into worker wake-ups and drains the stream when shutting down.
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

// connection returns the current client and session ID only while a usable session exists.
func (c *zooKeeperClient) connection() (*zk.Conn, int64, error) {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil || conn.State() != zk.StateHasSession {
		return nil, 0, zooKeeperError("session", zk.ErrConnectionClosed)
	}
	return conn, conn.SessionID(), nil
}

// available reports whether the client currently has a usable ZooKeeper session.
func (c *zooKeeperClient) available() bool {
	_, _, err := c.connection()
	return err == nil
}

// events exposes coalesced SDK notifications that wake the publication worker.
func (c *zooKeeperClient) events() <-chan struct{} {
	return c.wake
}

// close cancels the connection loop and waits for it to stop within the caller's deadline.
func (c *zooKeeperClient) close(ctx context.Context) error {
	c.cancel()
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return zooKeeperError("close", ctx.Err())
	}
}

// checkZooKeeperSession rejects operations whose original usable session has changed or expired.
func checkZooKeeperSession(conn *zk.Conn, session int64) error {
	if conn.State() != zk.StateHasSession || conn.SessionID() != session {
		return zooKeeperError("session changed", zk.ErrSessionExpired)
	}
	return nil
}

// read loads a publication node under the configured base path using the current session.
func (c *zooKeeperClient) read(ctx context.Context, name string) (zNode, error) {
	conn, session, err := c.connection()
	if err != nil {
		return zNode{}, err
	}
	return c.readOn(ctx, conn, session, path.Join(c.config.BasePath, name))
}

// readOn syncs an existing ancestor before reading and rejects session changes or non-persistent nodes.
func (c *zooKeeperClient) readOn(ctx context.Context, conn *zk.Conn, session int64, nodePath string) (zNode, error) {
	syncPath := nodePath
	// A missing target still needs a sync barrier before its absence can be trusted.
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

// ensureParents creates missing persistent ancestors and checks that the same session remains usable.
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

// write creates or version-checks a persistent node and reports whether the result is confirmed or uncertain.
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
			// A successful response from an old session cannot safely confirm the current node state.
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

// classifyWriteOutcome treats explicit server rejections as not executed and other failures as uncertain.
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

// Error reports a safe failure category, including details only for local integrity checks.
func (e *zooKeeperOperationError) Error() string {
	if e.category == "invalid" {
		return "ZooKeeper integrity failed: " + e.cause.Error()
	}
	return "ZooKeeper " + e.operation + " failed (" + e.category + ")"
}

// Unwrap exposes the underlying ZooKeeper error for matching and retry decisions.
func (e *zooKeeperOperationError) Unwrap() error {
	return e.cause
}

// zooKeeperError wraps an operation failure with its category while preserving the original cause.
func zooKeeperError(operation string, err error) error {
	return &zooKeeperOperationError{operation: operation, category: zooKeeperErrorCategory(err), cause: err}
}

// zooKeeperErrorCategory maps session, access, timeout and version errors to stable log categories.
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

// integrityError marks a locally detected metadata or node inconsistency as an invalid-state failure.
func integrityError(message string) error {
	return &zooKeeperOperationError{operation: "integrity", category: "invalid", cause: errors.New(message)}
}

type zooKeeperLogHandler struct {
	logger zerolog.Logger
	attrs  []slog.Attr
}

// Enabled checks the SDK log level against the configured zerolog threshold.
func (h *zooKeeperLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return h.logger.GetLevel() <= zooKeeperLogLevel(level)
}

// zooKeeperLogLevel maps slog severity to the corresponding zerolog level.
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

// Handle forwards SDK messages and nested error attributes to zerolog without copying other attributes.
func (h *zooKeeperLogHandler) Handle(_ context.Context, record slog.Record) error {
	var sdkError error
	var addError func(slog.Attr)
	addError = func(attr slog.Attr) {
		value := attr.Value.Resolve()
		if value.Kind() == slog.KindGroup {
			for _, nested := range value.Group() {
				addError(nested)
			}
			return
		}
		if attr.Key == "error" {
			sdkError, _ = value.Any().(error)
		}
	}
	for _, attr := range h.attrs {
		addError(attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		addError(attr)
		return true
	})
	h.logger.WithLevel(zooKeeperLogLevel(record.Level)).Str("source", "zookeeper-sdk").
		Err(sdkError).Msg(record.Message)
	return nil
}

// WithAttrs returns an independent handler copy that also considers the supplied error attributes.
func (h *zooKeeperLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	copyHandler := *h
	copyHandler.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &copyHandler
}

// WithGroup keeps the same handler because forwarded errors do not retain slog group names.
func (h *zooKeeperLogHandler) WithGroup(_ string) slog.Handler {
	return h
}
