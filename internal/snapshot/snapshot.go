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

type proposal struct {
	previous head
	next     head
	started  time.Time
}

type worker struct {
	config      config.SubscriptionSnapshots
	store       snapshotStore
	starting    bool
	pending     *proposal
	abandoned   string
	lastSuccess time.Time
}

func newWorker(c config.SubscriptionSnapshots, store snapshotStore) *worker {
	return &worker{config: c, store: store, starting: true}
}

func (w *worker) refresh(ctx context.Context) error {
	started := time.Now()
	if w.pending != nil {
		return w.resolve(ctx)
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
			return err
		}
	}
	complete, err := w.complete(ctx, previous.Version)
	if err != nil {
		return err
	}
	source, err := w.store.readSource(ctx, w.config.MaxSnapshotBytes)
	if err != nil {
		return err
	}
	if !w.starting && complete && previous.Version.SourceHash == source.sourceHash() {
		log.Debug().Int64("documentCount", previous.Version.DocumentCount).Msg("Subscription snapshot source unchanged")
		return nil
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
	w.pending = &proposal{
		previous: previous, next: proposeHead(previous, next, w.config.MinimumRetainedSnapshots), started: started,
	}
	return w.resolve(ctx)
}

func (w *worker) resolve(ctx context.Context) error {
	current, err := w.store.readHead(ctx)
	if err != nil {
		return err
	}
	candidate := w.pending
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
	w.published(candidate.next)
	return nil
}

func (w *worker) confirmProposal(current head) error {
	if !sameHead(current, w.pending.next) {
		return errors.New("activated candidate has unexpected metadata or history; cleanup blocked")
	}
	w.published(current)
	return nil
}

func (w *worker) published(current head) {
	duration := time.Since(w.pending.started)
	w.pending = nil
	w.starting = false
	w.lastSuccess = time.Now().UTC()
	log.Info().Str("snapshotId", current.Version.SnapshotID).Int64("documentCount", current.Version.DocumentCount).
		Str("sourceCollection", w.config.SourceCollection).Str("snapshotCollection", w.config.SnapshotCollection).
		Str("headCollection", w.config.HeadCollection).Dur("durationMs", duration).
		Time("lastSuccess", w.lastSuccess).Msg("Subscription snapshot published")
}

func (w *worker) complete(ctx context.Context, version descriptor) (bool, error) {
	if version.SnapshotID == "" {
		return false, nil
	}
	count, err := w.store.countSnapshot(ctx, version.SnapshotID)
	return count == version.DocumentCount, err
}

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

func (w *worker) deleteUnpublished(ctx context.Context, current head) error {
	if containsSnapshot(current, w.abandoned) {
		return errors.New("unpublished candidate unexpectedly appears in head history; cleanup blocked")
	}
	if err := w.deleteVersion(ctx, current, w.abandoned); err != nil {
		return err
	}
	w.abandoned = ""
	return nil
}

func (w *worker) cleanup(ctx context.Context, now time.Time) error {
	if w.starting || w.pending != nil {
		return errors.New("cleanup blocked until this process has acknowledged its activation")
	}
	current, err := w.store.readHead(ctx)
	if err != nil {
		return err
	}
	if current.Version.SnapshotID == "" {
		return errors.New("active head was reset to bootstrap; cleanup blocked")
	}
	for _, version := range current.RecentSnapshots {
		complete, err := w.complete(ctx, version)
		if err != nil {
			return err
		}
		if !complete {
			return errors.New("protected snapshot is incomplete; cleanup blocked")
		}
	}
	cutoff := now.Add(-w.config.RetentionTime)
	return w.store.visitSnapshotIDs(ctx, func(value string) error {
		id, err := canonicalID(value)
		if err != nil {
			return err
		}
		if containsSnapshot(current, value) || !id.Timestamp().Before(cutoff) {
			return nil
		}
		return w.deleteVersion(ctx, current, value)
	})
}

func (w *worker) deleteVersion(ctx context.Context, expected head, id string) error {
	for {
		current, err := w.store.readHead(ctx)
		if err != nil {
			return err
		}
		if !sameHead(current, expected) || containsSnapshot(current, id) {
			return errors.New("head or protected history changed during cleanup; deletion aborted")
		}
		deleted, err := w.store.deleteBatch(ctx, id)
		if err != nil {
			return err
		}
		if deleted == 0 {
			return nil
		}
	}
}
