// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog/log"
)

var errCleanupDeferred = errors.New("cleanup deferred until ZooKeeper references are safely resolved")

const (
	preparedNode  = "prepared"
	activatedNode = "activated"
)

type zState struct {
	prepared  zNode
	activated zNode
}

func sameState(a, b zState) bool {
	return sameNode(a.prepared, b.prepared) && sameNode(a.activated, b.activated)
}

type zOperation struct {
	name      string
	expected  zNode
	uncertain bool
}

type zCandidate struct {
	value          descriptor
	started        time.Time
	preparedAt     time.Time
	mongoConfirmed bool
	operation      *zOperation
}

type publication struct {
	transport      zooKeeperTransport
	delay          time.Duration
	observed       *zState
	pending        *zCandidate
	latest         descriptor
	degraded       bool
	firstActivated bool
	retryBlocked   bool
	seenPrepared   bool
	seenActivated  bool
}

func newPublication(transport zooKeeperTransport, delay time.Duration) *publication {
	return &publication{transport: transport, delay: delay}
}

func (p *publication) deadline() time.Time {
	if p.pending == nil || p.pending.preparedAt.IsZero() || p.retryBlocked {
		return time.Time{}
	}
	return p.pending.preparedAt.Add(p.delay)
}

func (p *publication) phase() string {
	if p.pending == nil {
		return "reconcile"
	}
	if p.pending.operation != nil {
		return p.pending.operation.name
	}
	if p.pending.preparedAt.IsZero() {
		return preparedNode
	}
	return activatedNode
}

func (p *publication) degrade(err error) {
	if !p.degraded {
		log.Warn().Err(err).Str("phase", p.phase()).Str("snapshotId", p.latest.SnapshotID).
			Msg("Subscription snapshots continuing with MongoDB fallback")
	}
	p.degraded = true
	p.retryBlocked = true
}

func (p *publication) mongoPublished(value descriptor) {
	p.latest = value
	if p.pending != nil && sameDescriptor(p.pending.value, value) {
		p.pending.mongoConfirmed = true
	}
}

func (w *worker) canPublishMongo() bool {
	p := w.publication
	if p == nil || p.degraded {
		return true
	}
	return p.pending != nil && w.pending != nil &&
		sameDescriptor(p.pending.value, w.pending.next.Version) &&
		!p.pending.preparedAt.IsZero() && !time.Now().Before(p.pending.preparedAt.Add(p.delay))
}

func (w *worker) progressZooKeeper(ctx context.Context) error {
	p := w.publication
	if !p.transport.available() {
		return zooKeeperError("session", errZooKeeperUnavailable)
	}
	if err := w.readPublicationState(ctx); err != nil {
		return err
	}
	if p.pending == nil {
		if err := w.selectZooKeeperCandidate(ctx); err != nil {
			return err
		}
	}
	if p.pending == nil {
		return nil
	}
	if p.pending.preparedAt.IsZero() {
		return p.writeNode(ctx, preparedNode)
	}
	if !p.observed.prepared.exists || !sameDescriptor(p.observed.prepared.value, p.pending.value) {
		return integrityError("prepared does not match the fixed ZooKeeper candidate")
	}
	if time.Now().Before(p.pending.preparedAt.Add(p.delay)) || !p.pending.mongoConfirmed {
		return nil
	}
	complete, err := w.complete(ctx, p.pending.value)
	if err != nil {
		return err
	}
	if !complete {
		return integrityError("fixed ZooKeeper candidate is incomplete")
	}
	if err := p.writeNode(ctx, activatedNode); err != nil {
		return err
	}
	if err := w.selectZooKeeperCandidate(ctx); err != nil {
		return err
	}
	if p.pending != nil {
		return p.writeNode(ctx, preparedNode)
	}
	return nil
}

func (w *worker) selectZooKeeperCandidate(ctx context.Context) error {
	p := w.publication
	if w.starting && (w.pending == nil || p.degraded) {
		return nil
	}
	var value descriptor
	confirmed := false
	if w.pending != nil && !p.degraded {
		value = w.pending.next.Version
	} else {
		current, err := w.store.readHead(ctx)
		if err != nil {
			return err
		}
		value = current.Version
		if p.latest.SnapshotID != "" && !sameDescriptor(value, p.latest) {
			return integrityError("MongoDB head is not a confirmed publication of this worker")
		}
		confirmed = true
		p.latest = value
	}
	if value.SnapshotID == "" {
		return nil
	}
	if confirmed && p.observed.activated.exists && sameDescriptor(p.observed.activated.value, value) {
		if p.degraded {
			log.Info().Str("snapshotId", value.SnapshotID).
				Msg("Subscription snapshots returned to synchronized publication")
		}
		p.degraded = false
		if w.pending == nil {
			return nil
		}
		value = w.pending.next.Version
		confirmed = false
	}
	complete, err := w.complete(ctx, value)
	if err != nil {
		return err
	}
	if !complete {
		return integrityError("ZooKeeper publication target is incomplete")
	}
	p.pending = &zCandidate{value: value, started: time.Now(), mongoConfirmed: confirmed}
	if p.degraded {
		log.Info().Str("snapshotId", value.SnapshotID).Str("mongoSnapshotId", p.latest.SnapshotID).
			Msg("Subscription snapshot ZooKeeper catch-up started")
	}
	return nil
}

func (w *worker) readPublicationState(ctx context.Context) error {
	p := w.publication
	prepared, err := p.transport.read(ctx, preparedNode)
	if err != nil {
		return err
	}
	if err := p.checkNode(preparedNode, prepared); err != nil {
		return err
	}
	p.observeNode(preparedNode, prepared)
	activated, err := p.transport.read(ctx, activatedNode)
	if err != nil {
		return err
	}
	if err := p.checkNode(activatedNode, activated); err != nil {
		return err
	}
	p.observeNode(activatedNode, activated)
	if prepared.session != activated.session {
		return zooKeeperError("read session changed", errors.New("ZooKeeper session changed between node reads"))
	}
	for _, node := range []zNode{prepared, activated} {
		if node.exists {
			complete, err := w.complete(ctx, node.value)
			if err != nil {
				return err
			}
			if !complete {
				return integrityError("ZooKeeper references an incomplete snapshot")
			}
		}
	}
	p.confirmReadBack()
	return nil
}

func (p *publication) observeNode(name string, node zNode) {
	if p.observed == nil {
		p.observed = &zState{}
	}
	if name == preparedNode {
		p.observed.prepared = node
		p.seenPrepared = true
	} else {
		p.observed.activated = node
		p.seenActivated = true
	}
}

func (p *publication) checkNode(name string, current zNode) error {
	if p.observed == nil || name == preparedNode && !p.seenPrepared || name == activatedNode && !p.seenActivated {
		return nil
	}
	previous := p.observed.prepared
	if name == activatedNode {
		previous = p.observed.activated
	}
	if sameNode(previous, current) {
		return nil
	}
	if p.pending != nil && p.pending.operation != nil && p.pending.operation.name == name {
		operation := p.pending.operation
		if sameNode(operation.expected, current) || matchesWrite(current, operation.expected, p.pending.value) {
			return nil
		}
	}
	return integrityError("ZooKeeper node disappeared or has an unknown identity, version or descriptor")
}

func matchesWrite(current, expected zNode, value descriptor) bool {
	if !current.exists || !sameDescriptor(current.value, value) {
		return false
	}
	if !expected.exists {
		return current.version == 0
	}
	return current.czxid == expected.czxid && current.version == expected.version+1
}

func (p *publication) confirmReadBack() {
	if p.pending == nil || p.pending.operation == nil {
		return
	}
	operation := p.pending.operation
	current := p.observed.prepared
	if operation.name == activatedNode {
		current = p.observed.activated
	}
	if matchesWrite(current, operation.expected, p.pending.value) {
		p.confirmNode(operation.name)
	}
}

func (p *publication) writeNode(ctx context.Context, name string) error {
	candidate := p.pending
	current := p.observed.prepared
	if name == activatedNode {
		current = p.observed.activated
	}
	if current.exists && sameDescriptor(current.value, candidate.value) && candidate.operation == nil {
		p.confirmNode(name)
		return nil
	}
	if candidate.operation == nil {
		candidate.operation = &zOperation{name: name, expected: current}
	}
	operation := candidate.operation
	operation.expected.session = current.session
	next, outcome, err := p.transport.write(ctx, name, candidate.value, operation.expected)
	if err != nil {
		if outcome == writeUncertain {
			operation.uncertain = true
		}
		if outcome == writeNotExecuted && !operation.uncertain && name == preparedNode && candidate.preparedAt.IsZero() {
			p.pending = nil
		}
		return err
	}
	if outcome != writeConfirmed || !matchesWrite(next, operation.expected, candidate.value) {
		operation.uncertain = true
		return integrityError("ZooKeeper write did not confirm the expected node transition")
	}
	if name == preparedNode {
		p.observed.prepared = next
	} else {
		p.observed.activated = next
	}
	p.confirmNode(name)
	return nil
}

func (p *publication) confirmNode(name string) {
	candidate := p.pending
	candidate.operation = nil
	if name == preparedNode {
		if candidate.preparedAt.IsZero() {
			candidate.preparedAt = time.Now()
			log.Info().Str("snapshotId", candidate.value.SnapshotID).
				Dur("durationMs", time.Since(candidate.started)).
				Msg("Subscription snapshot ZooKeeper prepared")
		}
		return
	}
	log.Info().Str("snapshotId", candidate.value.SnapshotID).Int64("documentCount", candidate.value.DocumentCount).
		Dur("durationMs", time.Since(candidate.started)).
		Msg("Subscription snapshot ZooKeeper activated")
	p.pending = nil
	p.firstActivated = true
}

func (w *worker) protectedZooKeeper(ctx context.Context) (zState, error) {
	p := w.publication
	if p == nil {
		return zState{}, nil
	}
	if !p.firstActivated || p.pending != nil || w.pending != nil {
		return zState{}, errCleanupDeferred
	}
	readCtx, cancel := context.WithTimeout(ctx, min(zooKeeperIOTimeout, w.config.CleanupTimeout))
	defer cancel()
	if err := w.readPublicationState(readCtx); err != nil {
		return zState{}, errors.Join(errCleanupDeferred, err)
	}
	if !p.observed.activated.exists || !p.observed.prepared.exists {
		return zState{}, errors.Join(errCleanupDeferred, integrityError("ZooKeeper protection references are missing"))
	}
	if p.latest.SnapshotID != "" {
		complete, err := w.complete(ctx, p.latest)
		if err != nil {
			return zState{}, errors.Join(errCleanupDeferred, err)
		}
		if !complete {
			return zState{}, errors.Join(errCleanupDeferred, integrityError("latest catch-up target is incomplete"))
		}
	}
	return *p.observed, nil
}

func (w *worker) protectedByZooKeeper(state zState, id string) bool {
	return state.prepared.exists && state.prepared.value.SnapshotID == id ||
		state.activated.exists && state.activated.value.SnapshotID == id ||
		w.publication != nil && w.publication.latest.SnapshotID == id
}
