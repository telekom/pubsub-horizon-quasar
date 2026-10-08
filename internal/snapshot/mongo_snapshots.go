// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type snapshotStore interface {
	readHead(context.Context) (head, error)
	readSource(context.Context, int64) (*sourceBuffer, error)
	insertSnapshot(context.Context, string, *sourceBuffer) error
	activate(context.Context, string, head) (bool, error)
	countSnapshot(context.Context, string) (int64, error)
	visitSnapshotIDs(context.Context, func(string) error) error
	deleteBatch(context.Context, string) (int64, error)
}

type publicationReason string

const (
	reasonInitial               publicationReason = "initial"
	reasonSnapshotCountMismatch publicationReason = "snapshot_count_mismatch"
	reasonRestart               publicationReason = "restart"
	reasonSourceChanged         publicationReason = "source_changed"
)

type proposal struct {
	previous head
	next     head
	started  time.Time
	reason   publicationReason
}

type worker struct {
	config      config.SubscriptionSnapshots
	store       snapshotStore
	starting    bool
	proposal    *proposal
	abandoned   string
	cleanupDue  bool
	lastSuccess time.Time
	publication *zooKeeperPublication
}

// newWorker creates a worker that forces its first snapshot before allowing cleanup.
func newWorker(c config.SubscriptionSnapshots, store snapshotStore) *worker {
	return &worker{config: c, store: store, starting: true}
}

// createSnapshot buffers the source and inserts a new version when starting, repairing or detecting a change.
// It keeps the old head active until the new proposal is resolved.
func (w *worker) createSnapshot(ctx context.Context) error {
	started := time.Now()
	if w.proposal != nil {
		return errors.New("snapshot creation blocked until the open MongoDB proposal is resolved")
	}
	previous, err := w.store.readHead(ctx)
	if err != nil {
		return err
	}
	if !w.starting && previous.Version.SnapshotID == "" {
		return errors.New("active head was reset to bootstrap; restore publication metadata")
	}
	if w.abandoned != "" {
		if err := w.deleteUnpublished(ctx, previous); err != nil {
			if !errors.Is(err, errCleanupDeferred) {
				return err
			}
			log.Warn().Err(err).Str("snapshotId", w.abandoned).Msg("Subscription snapshot orphan cleanup deferred")
		}
	}
	complete, err := snapshotComplete(ctx, w.store, previous.Version)
	if err != nil {
		return err
	}
	source, err := w.store.readSource(ctx, w.config.MaxSnapshotBytes)
	if err != nil {
		return err
	}
	if !w.starting && complete && previous.Version.SourceHash == source.sourceHash() {
		log.Debug().Str("snapshotId", previous.Version.SnapshotID).Int64("documentCount", previous.Version.DocumentCount).
			Str("sourceCollection", w.config.SourceCollection).Str("snapshotCollection", w.config.SnapshotCollection).
			Str("headCollection", w.config.HeadCollection).Dur("durationMs", time.Since(started)).
			Time("lastSuccess", w.lastSuccess).Msg("Subscription snapshot source unchanged")
		return nil
	}
	reason := reasonSourceChanged
	switch {
	case previous.Version.SnapshotID == "":
		reason = reasonInitial
	case !complete:
		reason = reasonSnapshotCountMismatch
	case w.starting:
		reason = reasonRestart
	}
	id := primitive.NewObjectID()
	next := descriptor{
		SnapshotID: id.Hex(), SourceHash: source.sourceHash(),
		DocumentCount: int64(len(source.documents)), CreatedAt: id.Timestamp(),
	}
	if err := w.store.insertSnapshot(ctx, next.SnapshotID, source); err != nil {
		// No activation has been sent for this ID. A timed-out insert can only leave orphan rows.
		w.abandoned = next.SnapshotID
		return err
	}
	w.proposal = &proposal{
		previous: previous, next: proposeHead(previous, next, w.config.MinimumRetainedSnapshots),
		started: started, reason: reason,
	}
	log.Info().Str("snapshotId", next.SnapshotID).Int64("documentCount", next.DocumentCount).
		Str("snapshotReason", string(reason)).
		Str("sourceCollection", w.config.SourceCollection).Str("snapshotCollection", w.config.SnapshotCollection).
		Str("headCollection", w.config.HeadCollection).Dur("durationMs", time.Since(started)).
		Msg("Subscription snapshot created")
	return nil
}

// resolve confirms or applies the pending head update, retaining uncertain proposals for later read-back.
func (w *worker) resolve(ctx context.Context) error {
	current, err := w.store.readHead(ctx)
	if err != nil {
		return err
	}
	candidate := w.proposal
	if current.Version.SnapshotID == candidate.next.Version.SnapshotID {
		return w.confirmProposal(current)
	}
	if !sameHead(current, candidate.previous) {
		if !w.starting || current.Version.SnapshotID == "" || current.Version.SnapshotID == candidate.previous.Version.SnapshotID ||
			containsSnapshot(current, candidate.next.Version.SnapshotID) {

			return errors.New("unexpected activation conflict; publication and cleanup blocked")
		}
		// Only startup can encounter an outstanding CAS from the previous process.
		// Rebase after observing its committed head; never infer failure from an old-head read.
		candidate.previous = current
		candidate.next = proposeHead(current, candidate.next.Version, w.config.MinimumRetainedSnapshots)
	}
	matched, err := w.store.activate(ctx, candidate.previous.Version.SnapshotID, candidate.next)
	if err != nil {
		return err
	}
	if !matched {
		current, err := w.store.readHead(ctx)
		if err != nil {
			return err
		}
		if current.Version.SnapshotID == candidate.next.Version.SnapshotID {
			return w.confirmProposal(current)
		}
		return errors.New("head CAS did not match; retaining the proposal for read-back and recovery")
	}
	return w.published(candidate.next)
}

// confirmProposal accepts a read-back only when both active metadata and history match the proposal.
func (w *worker) confirmProposal(current head) error {
	if !sameHead(current, w.proposal.next) {
		return errors.New("activated candidate has unexpected metadata or history; cleanup blocked")
	}
	return w.published(current)
}

// published records a confirmed MongoDB head, notifies ZooKeeper publication and schedules cleanup.
func (w *worker) published(current head) error {
	duration := time.Since(w.proposal.started)
	reason := w.proposal.reason
	w.proposal = nil
	w.starting = false
	w.cleanupDue = true
	w.lastSuccess = time.Now().UTC()
	if w.publication != nil {
		w.publication.mongoPublished(current.Version)
	}
	log.Info().Str("snapshotId", current.Version.SnapshotID).Int64("documentCount", current.Version.DocumentCount).
		Str("snapshotReason", string(reason)).
		Str("sourceCollection", w.config.SourceCollection).Str("snapshotCollection", w.config.SnapshotCollection).
		Str("headCollection", w.config.HeadCollection).Dur("durationMs", duration).
		Time("lastSuccess", w.lastSuccess).Msg("Subscription snapshot published")
	return nil
}

// cleanupPending clears the cleanup flag only after pending cleanup completes successfully.
func (w *worker) cleanupPending(ctx context.Context) error {
	if !w.cleanupDue {
		return nil
	}
	if err := w.cleanup(ctx); err != nil {
		return err
	}
	w.cleanupDue = false
	return nil
}

// snapshotComplete checks that a non-bootstrap version has its advertised document count.
func snapshotComplete(ctx context.Context, store snapshotStore, version descriptor) (bool, error) {
	if version.SnapshotID == "" {
		return false, nil
	}
	count, err := store.countSnapshot(ctx, version.SnapshotID)
	return count == version.DocumentCount, err
}

// containsSnapshot reports whether an ID belongs to the active head or its retained history.
func containsSnapshot(current head, id string) bool {
	if current.Version.SnapshotID == id {
		return true
	}
	for _, item := range current.RecentSnapshots {
		if item.SnapshotID == id {
			return true
		}
	}
	return false
}

// deleteUnpublished removes rows left by a failed insert only if publication references do not protect them.
func (w *worker) deleteUnpublished(ctx context.Context, current head) error {
	if containsSnapshot(current, w.abandoned) {
		return errors.New("unpublished candidate unexpectedly appears in head history; cleanup blocked")
	}
	if _, err := w.deleteVersion(ctx, current, w.abandoned); err != nil {
		return err
	}
	w.abandoned = ""
	return nil
}

// cleanup deletes unprotected versions after checking that the retained snapshots and references are safe.
func (w *worker) cleanup(ctx context.Context) error {
	started := time.Now()
	if w.starting || w.proposal != nil {
		return errors.Join(errCleanupDeferred, errors.New("cleanup blocked until this process has acknowledged its activation"))
	}
	current, err := w.store.readHead(ctx)
	if err != nil {
		return err
	}
	if current.Version.SnapshotID == "" {
		return errors.New("active head was reset to bootstrap; cleanup blocked")
	}
	protected, err := w.protectedZooKeeper(ctx)
	if err != nil {
		return err
	}
	for _, version := range current.RecentSnapshots {
		// Losing a retained version would leave readers without the promised fallback history.
		complete, err := snapshotComplete(ctx, w.store, version)
		if err != nil {
			return err
		}
		if !complete {
			return errors.New("protected snapshot is incomplete; cleanup blocked")
		}
	}
	var deletedDocuments int64
	if err := w.store.visitSnapshotIDs(ctx, func(value string) error {
		if _, err := canonicalID(value); err != nil {
			return err
		}
		if containsSnapshot(current, value) || w.publication.protects(protected, value) {
			return nil
		}
		deleted, err := w.deleteVersion(ctx, current, value)
		deletedDocuments += deleted
		return err
	}); err != nil {
		return err
	}
	log.Debug().Dur("durationMs", time.Since(started)).Int64("deletedDocuments", deletedDocuments).
		Str("sourceCollection", w.config.SourceCollection).
		Str("snapshotCollection", w.config.SnapshotCollection).
		Str("headCollection", w.config.HeadCollection).
		Msg("Subscription snapshot cleanup completed")
	return nil
}

// deleteVersion deletes one version in batches, rechecking MongoDB and ZooKeeper protection before each batch.
func (w *worker) deleteVersion(ctx context.Context, expected head, id string) (int64, error) {
	protected, err := w.protectedZooKeeper(ctx)
	if err != nil {
		return 0, err
	}
	if w.publication.protects(protected, id) {
		return 0, errors.Join(errCleanupDeferred, errors.New("snapshot is protected by ZooKeeper references or the latest catch-up target"))
	}
	var deletedDocuments int64
	for {
		current, err := w.store.readHead(ctx)
		if err != nil {
			return deletedDocuments, err
		}
		if !sameHead(current, expected) || containsSnapshot(current, id) {
			return deletedDocuments, errors.New("head or protected history changed during cleanup; deletion aborted")
		}
		latestProtection, err := w.protectedZooKeeper(ctx)
		if err != nil {
			return deletedDocuments, err
		}
		// Any reference change invalidates the safety decision made before deletion started.
		if !sameState(protected, latestProtection) || w.publication.protects(latestProtection, id) {
			return deletedDocuments, errors.Join(errCleanupDeferred, errors.New("ZooKeeper references changed during deletion"))
		}

		deleted, err := w.store.deleteBatch(ctx, id)
		if err != nil {
			return deletedDocuments, err
		}
		deletedDocuments += deleted
		if deleted == 0 {
			return deletedDocuments, nil
		}
	}
}

// proposedSnapshot returns the pending MongoDB version, or an empty descriptor when no proposal is open.
func (w *worker) proposedSnapshot() descriptor {
	if w.proposal == nil {
		return descriptor{}
	}
	return w.proposal.next.Version
}

// progressZooKeeper advances publication using the current proposal and the worker's startup state.
func (w *worker) progressZooKeeper(ctx context.Context) error {
	return w.publication.progress(ctx, w.store, w.proposedSnapshot(), w.starting)
}

// protectedZooKeeper reads safe cleanup references within a bounded timeout, or skips absent publication.
func (w *worker) protectedZooKeeper(ctx context.Context) (zState, error) {
	if w.publication == nil {
		return zState{}, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, min(zooKeeperIOTimeout, w.config.CleanupTimeout))
	defer cancel()
	return w.publication.protection(readCtx, w.store, w.proposal != nil)
}
