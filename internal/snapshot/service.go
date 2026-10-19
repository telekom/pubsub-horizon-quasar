// SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/Shopify/zk"
	zlog "github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/snapshot/mongo"
	"github.com/telekom/quasar/internal/snapshot/state"
	"github.com/telekom/quasar/internal/snapshot/zoo"
	"go.mongodb.org/mongo-driver/bson"
)

var (
	log = zlog.With().Str("pkg", "snapshot").Logger()
)

type Service struct {
	mongo *mongo.Client
	zoo   *zoo.Client

	head   *state.Head
	headMu sync.Mutex

	lastResumeToken   *state.ResumeToken
	lastResumeTokenMu sync.Mutex

	activationDelay     time.Duration
	activationPending   bool
	activationPendingMu sync.Mutex

	svcCtx       context.Context
	svcCtxCancel context.CancelFunc
	svcWg        sync.WaitGroup

	svcRebuildInterval time.Duration
	svcCleanupInterval time.Duration
}

func NewService(mongo *mongo.Client, zoo *zoo.Client, rebuildInterval time.Duration, activationDelay time.Duration, cleanupInterval time.Duration) *Service {
	ctx, cancel := context.WithCancel(context.Background())

	return &Service{
		mongo: mongo,
		zoo:   zoo,

		head:   nil,
		headMu: sync.Mutex{},

		lastResumeToken:   nil,
		lastResumeTokenMu: sync.Mutex{},

		activationDelay:     activationDelay,
		activationPending:   false,
		activationPendingMu: sync.Mutex{},

		svcCtx:             ctx,
		svcCtxCancel:       cancel,
		svcRebuildInterval: rebuildInterval,
		svcCleanupInterval: cleanupInterval,
		svcWg:              sync.WaitGroup{},
	}
}

func (s *Service) Start() error {
	ok, err := s.fetchHead()
	if err != nil {
		return err
	}

	if !ok {
		if err := s.mongo.Connect([]byte{}); err != nil {
			return err
		}

		if err := s.initializeHead(); err != nil {
			return err
		}
		log.Info().Msg("head initialized")
	} else {
		log.Info().Msg("existing head found")

		bsonResumeToken, err := s.head.ResumeToken.Bson()
		if err != nil {
			return err
		}

		if err := s.mongo.Connect(bsonResumeToken); err != nil {
			return err
		}
	}

	activationPending, err := s.isActivationPending()
	if err != nil {
		return err
	}

	if activationPending {
		s.activationPending = true
	}

	s.svcWg.Go(s.waitForChanges)
	s.svcWg.Go(s.runCleanupJob)
	return nil
}

func (s *Service) initializeHead() error {
	snapshotId, documentCount, err := s.mongo.CreateSnapshot()
	if err != nil {
		return err
	}

	head := &state.Head{
		HistoryEntry: state.HistoryEntry{
			ID:            snapshotId,
			DocumentCount: documentCount,
			Created:       time.Now(),
		},
		History: make([]state.HistoryEntry, 0),
	}

	resumeToken, err := state.ParseResumeToken(s.mongo.ResumeToken())
	if err != nil {
		return err
	}
	head.ResumeToken = resumeToken

	bytes, err := json.Marshal(head)
	if err != nil {
		return err
	}

	if err := s.zoo.Write(zoo.ZNodePrepared, bytes); err != nil {
		return err
	}

	s.head = head

	return nil
}

func (s *Service) fetchHead() (bool, error) {
	headBytes, err := s.zoo.Read(zoo.ZNodePrepared)
	if err != nil {
		if errors.Is(err, zk.ErrNoNode) {
			return false, nil
		}
		return false, err
	}

	var head state.Head
	if err := json.Unmarshal(headBytes, &head); err != nil {
		return false, err
	}

	s.head = &head

	return true, nil
}

func (s *Service) waitForChanges() {
	for {
		select {

		case <-time.After(s.svcRebuildInterval):
			if s.changesPresent() {
				s.headMu.Lock()
				currentHead := *s.head
				s.headMu.Unlock()

				s.lastResumeTokenMu.Lock()
				currentLastResumeToken := *s.lastResumeToken
				s.lastResumeTokenMu.Unlock()

				snapshotId, documentCount, err := s.mongo.CreateSnapshot()
				if err != nil {
					log.Error().Err(err).Msg("failed to create snapshot")
					continue
				}

				s.updateHead(&currentHead, snapshotId, documentCount, &currentLastResumeToken)

				bytes, err := json.Marshal(currentHead)
				if err != nil {
					log.Error().Err(err).Msg("failed to marshal head")
					continue
				}

				if err := s.zoo.Write(zoo.ZNodePrepared, bytes); err != nil {
					log.Error().
						Str("snapshotId", snapshotId).
						Err(err).
						Msg("failed to write head")
				}

				log.Debug().
					Str("snapshotId", snapshotId).
					Int64("documentCount", documentCount).
					Msg("head updated")

				s.headMu.Lock()
				s.head = &currentHead
				s.headMu.Unlock()

				s.activationPendingMu.Lock()
				s.activationPending = true
				s.activationPendingMu.Unlock()
			} else {
				log.Debug().
					Msg("no changes therefore skipping snapshot creation")
			}

			s.activationPendingMu.Lock()
			activationPending := s.activationPending
			s.activationPendingMu.Unlock()

			if activationPending {
				s.activatePrepared(s.activationDelay)
			}

		case lastEvent := <-s.mongo.GetUpdates():
			var update bson.M
			if err := bson.Unmarshal(lastEvent, &update); err != nil {
				log.Error().
					Err(err).
					Msg("failed to unmarshal resumeToken")
				continue
			}

			resumeToken, err := state.ParseResumeToken(lastEvent)
			if err != nil {
				log.Error().
					Err(err).
					Msg("failed to parse resumeToken")
				continue
			}

			s.lastResumeTokenMu.Lock()
			s.lastResumeToken = resumeToken
			s.lastResumeTokenMu.Unlock()

			log.Debug().
				Str("resumeToken", resumeToken.Value).
				Msg("resumeToken updated")

		case <-s.svcCtx.Done():
			return
		}
	}
}

func (s *Service) runCleanupJob() {
	for {
		select {

		case <-time.After(s.svcCleanupInterval):
			s.headMu.Lock()

			retainedSnapshotIds := s.head.GetSnapshotIds()

			activatedHeadBytes, err := s.zoo.Read(zoo.ZNodeActivated)
			if err != nil && !errors.Is(err, zk.ErrNoNode) {
				log.Error().
					Err(err).
					Msg("failed to retrieve activated head during cleanup")
				s.headMu.Unlock()
				continue
			} else {
				var activatedHead state.Head
				if err := json.Unmarshal(activatedHeadBytes, &activatedHead); err != nil {
					log.Error().
						Err(err).
						Msg("failed to unmarshal the activated head")

					s.headMu.Unlock()
					continue
				}

				for _, snapshotId := range activatedHead.GetSnapshotIds() {
					if !slices.Contains(retainedSnapshotIds, snapshotId) {
						retainedSnapshotIds = append(retainedSnapshotIds, snapshotId)
					}
				}
			}

			result, err := s.mongo.DeleteStaleSnapshots(retainedSnapshotIds)
			if err != nil {
				log.Error().
					Err(err).
					Msg("cleanup of stale snapshots failed")
			} else {
				log.Debug().
					Int64("documentCount", result.DeletedCount).
					Msg("cleaned up stale snapshots")
			}

			s.headMu.Unlock()

		case <-s.svcCtx.Done():
			return

		}
	}
}

func (s *Service) updateHead(head *state.Head, snapshotId string, documentCount int64, resumeToken *state.ResumeToken) {
	head.History = append(head.History, state.HistoryEntry{
		ID:            head.ID,
		DocumentCount: head.DocumentCount,
		Created:       head.Created,
	})

	if len(head.History) > 3 {
		head.History = head.History[1:]
	}

	head.ID = snapshotId
	head.DocumentCount = documentCount
	head.ResumeToken = resumeToken
	head.Created = time.Now()
}

func (s *Service) changesPresent() bool {
	s.headMu.Lock()
	defer s.headMu.Unlock()

	s.lastResumeTokenMu.Lock()
	defer s.lastResumeTokenMu.Unlock()

	if s.lastResumeToken == nil || s.head.ResumeToken == nil {
		return false
	}

	return s.lastResumeToken.Value != s.head.ResumeToken.Value
}

func (s *Service) isActivationPending() (bool, error) {
	s.activationPendingMu.Lock()
	defer s.activationPendingMu.Unlock()

	preparedBytes, err := s.zoo.Read(zoo.ZNodePrepared)
	if err != nil {
		log.Error().
			Err(err).
			Msg("failed to read prepare-head while checking for pending activation")
		return false, err
	}

	activatedBytes, err := s.zoo.Read(zoo.ZNodeActivated)
	if err != nil {
		if errors.Is(err, zk.ErrNoNode) {
			log.Debug().
				Msg("activate-head node does not exist yet")
			return true, nil
		}

		log.Error().
			Err(err).
			Msg("failed to read activate-head while checking for pending activation")
		return false, err
	}

	if string(preparedBytes) != string(activatedBytes) {
		log.Debug().
			Msg("prepare and activate head do not match, activation will be scheduled")

		return true, nil
	}

	return false, nil
}

func (s *Service) activatePrepared(activationDelay time.Duration) {
	s.activationPendingMu.Lock()
	defer s.activationPendingMu.Unlock()

	preparedBytes, err := s.zoo.Read(zoo.ZNodePrepared)
	if err != nil {
		log.Error().
			Err(err).
			Msg("failed to read prepare-head during activation")
		return
	}

	log.Debug().
		Str("activationDelay", activationDelay.String()).
		Msg("waiting for activation delay to pass")
	time.Sleep(activationDelay)

	if err := s.zoo.Write(zoo.ZNodeActivated, preparedBytes); err != nil {
		log.Error().
			Err(err).
			Msg("failed to write activate-head during activation")
		return
	}

	s.activationPending = false
	log.Debug().
		Msg("activation succeeded")
}

func (s *Service) Stop() {
	s.svcCtxCancel()
	s.svcWg.Wait()
}
