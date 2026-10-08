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

// sameState compares both publication references by node identity, version and descriptor.
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

type zooKeeperPublication struct {
	transport           zooKeeperTransport
	delay               time.Duration
	observed            *zState
	candidate           *zCandidate
	latestConfirmedHead descriptor
	degraded            bool
	firstActivated      bool
	retryBlocked        bool
	seenPrepared        bool
	seenActivated       bool
}

// newZooKeeperPublication creates publication state with a fixed delay between preparation and activation.
func newZooKeeperPublication(transport zooKeeperTransport, delay time.Duration) *zooKeeperPublication {
	return &zooKeeperPublication{transport: transport, delay: delay}
}

// nextDeadline returns the candidate's activation time only when the required retries are not blocked.
func (p *zooKeeperPublication) nextDeadline(mongoRetryBlocked bool) time.Time {
	if p.candidate == nil || p.candidate.preparedAt.IsZero() || p.retryBlocked {
		return time.Time{}
	}
	if mongoRetryBlocked && !p.candidate.mongoConfirmed {
		return time.Time{}
	}
	return p.candidate.preparedAt.Add(p.delay)
}

// allowRetry releases the publication retry gate for a new refresh or recovery event.
func (p *zooKeeperPublication) allowRetry() {
	p.retryBlocked = false
}

// blockRetry prevents further publication attempts until a later eligible event.
func (p *zooKeeperPublication) blockRetry() {
	p.retryBlocked = true
}

// canRetry reports whether publication may attempt ZooKeeper work in the current event window.
func (p *zooKeeperPublication) canRetry() bool {
	return !p.retryBlocked
}

// needsPreparation reports whether a new candidate can be selected without a retry block.
func (p *zooKeeperPublication) needsPreparation() bool {
	return p.candidate == nil && p.canRetry()
}

// activationDue requires a confirmed MongoDB candidate, an elapsed preparation delay and an open retry gate.
func (p *zooKeeperPublication) activationDue() bool {
	return p.candidate != nil && p.candidate.mongoConfirmed && p.canRetry() &&
		!p.candidate.preparedAt.IsZero() && !time.Now().Before(p.candidate.preparedAt.Add(p.delay))
}

// snapshotID selects the fixed candidate, proposal or latest confirmed head for operation logging.
func (p *zooKeeperPublication) snapshotID(proposed descriptor) string {
	if p.candidate != nil {
		return p.candidate.value.SnapshotID
	}
	if proposed.SnapshotID != "" {
		return proposed.SnapshotID
	}
	return p.latestConfirmedHead.SnapshotID
}

// phase names the pending write or next publication stage for logs.
func (p *zooKeeperPublication) phase() string {
	if p.candidate == nil {
		return "reconcile"
	}
	if p.candidate.operation != nil {
		return p.candidate.operation.name
	}
	if p.candidate.preparedAt.IsZero() {
		return preparedNode
	}
	return activatedNode
}

// degrade enters MongoDB fallback and blocks retries, logging only the transition into degraded mode.
func (p *zooKeeperPublication) degrade(err error) {
	if !p.degraded {
		log.Warn().Err(err).Str("phase", p.phase()).Str("snapshotId", p.latestConfirmedHead.SnapshotID).
			Msg("Subscription snapshots continuing with MongoDB fallback")
	}
	p.degraded = true
	p.retryBlocked = true
}

// mongoPublished records the latest confirmed head and marks a matching fixed candidate as confirmed.
func (p *zooKeeperPublication) mongoPublished(value descriptor) {
	p.latestConfirmedHead = value
	if p.candidate != nil && sameDescriptor(p.candidate.value, value) {
		p.candidate.mongoConfirmed = true
	}
}

// mongoAllowed permits fallback immediately or waits until the matching candidate's preparation delay ends.
func (p *zooKeeperPublication) mongoAllowed(proposed descriptor) bool {
	if p == nil || p.degraded {
		return true
	}
	return p.candidate != nil && proposed.SnapshotID != "" &&
		sameDescriptor(p.candidate.value, proposed) &&
		!p.candidate.preparedAt.IsZero() && !time.Now().Before(p.candidate.preparedAt.Add(p.delay))
}

// progress reads back publication state, prepares a fixed candidate and activates it after MongoDB confirmation.
func (p *zooKeeperPublication) progress(ctx context.Context, store snapshotStore, proposed descriptor, starting bool) error {
	if !p.transport.available() {
		return zooKeeperError("session", errZooKeeperUnavailable)
	}
	if err := p.readState(ctx, store); err != nil {
		return err
	}
	if p.candidate == nil {
		if err := p.selectCandidate(ctx, store, proposed, starting); err != nil {
			return err
		}
	}
	if p.candidate == nil {
		return nil
	}
	if p.candidate.preparedAt.IsZero() {
		return p.writeNode(ctx, preparedNode)
	}
	if !p.observed.prepared.exists || !sameDescriptor(p.observed.prepared.value, p.candidate.value) {
		return integrityError("prepared does not match the fixed ZooKeeper candidate")
	}
	if time.Now().Before(p.candidate.preparedAt.Add(p.delay)) || !p.candidate.mongoConfirmed {
		return nil
	}
	complete, err := snapshotComplete(ctx, store, p.candidate.value)
	if err != nil {
		return err
	}
	if !complete {
		return integrityError("fixed ZooKeeper candidate is incomplete")
	}
	if err := p.writeNode(ctx, activatedNode); err != nil {
		return err
	}
	if err := p.selectCandidate(ctx, store, proposed, starting); err != nil {
		return err
	}
	if p.candidate != nil {
		return p.writeNode(ctx, preparedNode)
	}
	return nil
}

// selectCandidate chooses a complete proposal or confirmed fallback head and keeps it fixed until activation.
func (p *zooKeeperPublication) selectCandidate(ctx context.Context, store snapshotStore, proposed descriptor, starting bool) error {
	if starting && (proposed.SnapshotID == "" || p.degraded) {
		return nil
	}
	var value descriptor
	confirmed := false
	if proposed.SnapshotID != "" && !p.degraded {
		value = proposed
	} else {
		current, err := store.readHead(ctx)
		if err != nil {
			return err
		}
		value = current.Version
		if p.latestConfirmedHead.SnapshotID != "" && !sameDescriptor(value, p.latestConfirmedHead) {
			return integrityError("MongoDB head is not a confirmed publication of this worker")
		}
		confirmed = true
		p.latestConfirmedHead = value
	}
	if value.SnapshotID == "" {
		return nil
	}
	if confirmed && p.observed.activated.exists && sameDescriptor(p.observed.activated.value, value) {
		// Catch-up must finish before a newer, unconfirmed proposal resumes normal publication.
		if p.degraded {
			log.Info().Str("snapshotId", value.SnapshotID).
				Msg("Subscription snapshots returned to synchronized publication")
		}
		p.degraded = false
		if proposed.SnapshotID == "" {
			return nil
		}
		value = proposed
		confirmed = false
	}
	complete, err := snapshotComplete(ctx, store, value)
	if err != nil {
		return err
	}
	if !complete {
		return integrityError("ZooKeeper publication target is incomplete")
	}
	p.candidate = &zCandidate{value: value, started: time.Now(), mongoConfirmed: confirmed}
	if p.degraded {
		log.Info().Str("snapshotId", value.SnapshotID).Str("mongoSnapshotId", p.latestConfirmedHead.SnapshotID).
			Msg("Subscription snapshot ZooKeeper catch-up started")
	}
	return nil
}

// readState validates both references in one session, checks snapshot counts and resolves known write results.
func (p *zooKeeperPublication) readState(ctx context.Context, store snapshotStore) error {
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
			complete, err := snapshotComplete(ctx, store, node.value)
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

// observeNode stores a validated reference and remembers which publication nodes have been seen.
func (p *zooKeeperPublication) observeNode(name string, node zNode) {
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

// checkNode accepts unchanged references or the expected pending write and rejects unknown transitions.
func (p *zooKeeperPublication) checkNode(name string, current zNode) error {
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
	if p.candidate != nil && p.candidate.operation != nil && p.candidate.operation.name == name {
		operation := p.candidate.operation
		if sameNode(operation.expected, current) || matchesWrite(current, operation.expected, p.candidate.value) {
			return nil
		}
	}
	return integrityError("ZooKeeper node disappeared or has an unknown identity, version or descriptor")
}

// matchesWrite checks the exact identity and version transition expected from one create or update.
func matchesWrite(current, expected zNode, value descriptor) bool {
	if !current.exists || !sameDescriptor(current.value, value) {
		return false
	}
	if !expected.exists {
		return current.version == 0
	}
	return current.czxid == expected.czxid && current.version == expected.version+1
}

// confirmReadBack completes a pending write only when observed metadata proves its expected transition.
func (p *zooKeeperPublication) confirmReadBack() {
	if p.candidate == nil || p.candidate.operation == nil {
		return
	}
	operation := p.candidate.operation
	current := p.observed.prepared
	if operation.name == activatedNode {
		current = p.observed.activated
	}
	if matchesWrite(current, operation.expected, p.candidate.value) {
		p.confirmNode(operation.name)
	}
}

// writeNode tracks the expected node transition and preserves candidates whose write outcome is uncertain.
func (p *zooKeeperPublication) writeNode(ctx context.Context, name string) error {
	candidate := p.candidate
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
	// Retry in the current session while keeping the original node identity and version expectation.
	operation.expected.session = current.session
	next, outcome, err := p.transport.write(ctx, name, candidate.value, operation.expected)
	if err != nil {
		if outcome == writeUncertain {
			operation.uncertain = true
		}
		if outcome == writeNotExecuted && !operation.uncertain && name == preparedNode && candidate.preparedAt.IsZero() {
			// Only a first preparation that definitely did not execute can release the fixed candidate.
			p.candidate = nil
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

// confirmNode starts the delay after confirmed preparation or releases the candidate after activation.
func (p *zooKeeperPublication) confirmNode(name string) {
	candidate := p.candidate
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
	p.candidate = nil
	p.firstActivated = true
}

// protection returns verified cleanup references only after activation and with no unresolved candidate or proposal.
func (p *zooKeeperPublication) protection(ctx context.Context, store snapshotStore, proposalPending bool) (zState, error) {
	if !p.firstActivated || p.candidate != nil || proposalPending {
		return zState{}, errCleanupDeferred
	}
	if err := p.readState(ctx, store); err != nil {
		return zState{}, errors.Join(errCleanupDeferred, err)
	}
	if !p.observed.activated.exists || !p.observed.prepared.exists {
		return zState{}, errors.Join(errCleanupDeferred, integrityError("ZooKeeper protection references are missing"))
	}
	if p.latestConfirmedHead.SnapshotID != "" {
		complete, err := snapshotComplete(ctx, store, p.latestConfirmedHead)
		if err != nil {
			return zState{}, errors.Join(errCleanupDeferred, err)
		}
		if !complete {
			return zState{}, errors.Join(errCleanupDeferred, integrityError("latest catch-up target is incomplete"))
		}
	}
	return *p.observed, nil
}

// protects checks both ZooKeeper references and the latest confirmed head that catch-up may still need.
func (p *zooKeeperPublication) protects(state zState, id string) bool {
	return state.prepared.exists && state.prepared.value.SnapshotID == id ||
		state.activated.exists && state.activated.value.SnapshotID == id ||
		p != nil && p.latestConfirmedHead.SnapshotID == id
}
