// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/telekom/quasar/internal/config"
	"github.com/telekom/quasar/internal/utils"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

const shutdownTimeout = 10 * time.Second

type service struct {
	config   config.SubscriptionSnapshots
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
	client   *mongo.Client
	session  mongo.Session
	store    *mongoStore
	worker   *worker
}

// Start registers shutdown before starting the independent, sequential snapshot worker.
// Disabled configuration has no lifecycle or database side effects.
func Start(c config.SubscriptionSnapshots) error {
	if err := c.Validate(); err != nil {
		return err
	}
	logStartup(c)
	if !c.Enabled {
		return nil
	}
	s := newService(c)
	utils.RegisterShutdownHook(s.shutdown, 0)
	go s.run()
	return nil
}

func logStartup(c config.SubscriptionSnapshots) {
	if !c.Enabled {
		log.Info().Bool("enabled", false).Msg("Subscription snapshots disabled")
		return
	}
	log.Info().Bool("enabled", true).
		Str("database", c.Database).
		Str("sourceCollection", c.SourceCollection).
		Str("snapshotCollection", c.SnapshotCollection).
		Str("headCollection", c.HeadCollection).
		Str("initialStartDelay", c.InitialStartDelay.String()).
		Str("refreshInterval", c.RefreshInterval.String()).
		Str("cleanupInterval", c.CleanupInterval.String()).
		Str("retentionTime", c.RetentionTime.String()).
		Int("minimumRetainedSnapshots", c.MinimumRetainedSnapshots).
		Int64("maxSnapshotBytes", c.MaxSnapshotBytes).
		Str("operationTimeout", c.OperationTimeout.String()).
		Msg("Starting subscription snapshot worker")
}

func newService(c config.SubscriptionSnapshots) *service {
	ctx, cancel := context.WithCancel(context.Background())
	return &service{config: c, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (s *service) run() {
	defer close(s.done)
	defer s.disconnect()
	if !s.waitForStart() {
		return
	}
	refresh := time.NewTicker(s.config.RefreshInterval)
	defer refresh.Stop()
	s.loop(refresh.C)
}

func (s *service) waitForStart() bool {
	timer := time.NewTimer(s.config.InitialStartDelay)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return false
	case <-timer.C:
		return s.ctx.Err() == nil
	}
}

func (s *service) loop(refresh <-chan time.Time) {
	cleanup := time.NewTimer(s.config.CleanupInterval)
	defer cleanup.Stop()
	cleanupDue := true
	s.attemptRefresh()
	for {
		if cleanupDue {
			cleanupDue = !s.attemptCleanup()
			cleanup.Reset(s.config.CleanupInterval)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-refresh:
			s.attemptRefresh()
		case <-cleanup.C:
			cleanupDue = true
		}
	}
}

func (s *service) initialize(ctx context.Context) error {
	if s.client == nil {
		if err := s.connect(ctx); err != nil {
			if s.ctx.Err() == nil {
				log.Fatal().Err(err).Msg("Subscription snapshot connection failed")
			}
			return err
		}
	}
	if s.store == nil {
		s.store = newMongoStore(s.client, s.config)
	}
	if s.worker == nil {
		if err := s.withSession(ctx, s.store.setup); err != nil {
			return err
		}
		s.worker = newWorker(s.config, s.store)
	}
	return nil
}

func (s *service) connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	opts := options.Client().ApplyURI(s.config.URI).
		SetReadPreference(readpref.Primary()).
		SetServerSelectionTimeout(s.config.OperationTimeout).
		SetConnectTimeout(s.config.OperationTimeout)
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		return databaseError("connect dedicated subscription snapshot client", err)
	}
	s.client = client
	return databaseError("ping dedicated subscription snapshot client", client.Ping(ctx, nil))
}

func (s *service) withSession(ctx context.Context, action func(context.Context) error) error {
	if s.session == nil {
		session, err := s.client.StartSession(options.Session().SetCausalConsistency(true))
		if err != nil {
			return databaseError("start causally consistent session", err)
		}
		s.session = session
	}
	return mongo.WithSession(ctx, s.session, func(sessionContext mongo.SessionContext) error {
		return action(sessionContext)
	})
}

func (s *service) attemptRefresh() {
	ctx, cancel := context.WithTimeout(s.ctx, s.config.OperationTimeout)
	start := time.Now()
	err := s.initialize(ctx)
	if err == nil {
		err = s.withSession(ctx, s.worker.refresh)
	}
	cancel()
	if err != nil {
		s.logError("refresh", start, err)
		return
	}
	log.Info().Dur("durationMs", time.Since(start)).Str("sourceCollection", s.config.SourceCollection).
		Str("snapshotCollection", s.config.SnapshotCollection).Str("headCollection", s.config.HeadCollection).
		Time("lastSuccess", s.worker.lastSuccess).Msg("Subscription snapshot refresh completed")
}

func (s *service) attemptCleanup() bool {
	if s.worker == nil || s.worker.starting || s.worker.pending != nil {
		return false
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(s.ctx, s.config.OperationTimeout)
	defer cancel()
	err := s.withSession(ctx, func(ctx context.Context) error {
		return s.worker.cleanup(ctx, start)
	})
	if err != nil {
		s.logError("cleanup", start, err)
		return false
	}
	log.Info().Dur("durationMs", time.Since(start)).Str("sourceCollection", s.config.SourceCollection).
		Str("snapshotCollection", s.config.SnapshotCollection).Str("headCollection", s.config.HeadCollection).
		Msg("Subscription snapshot cleanup completed")
	return true
}

func (s *service) logError(operation string, start time.Time, err error) {
	log.Error().Err(err).Str("operation", operation).Dur("durationMs", time.Since(start)).
		Str("sourceCollection", s.config.SourceCollection).Str("snapshotCollection", s.config.SnapshotCollection).
		Str("headCollection", s.config.HeadCollection).Msg("Subscription snapshot operation failed")
}

func (s *service) shutdown() {
	s.stopOnce.Do(func() {
		s.cancel()
		timer := time.NewTimer(shutdownTimeout)
		defer timer.Stop()
		select {
		case <-s.done:
		case <-timer.C:
			log.Error().Msg("Subscription snapshot shutdown exceeded 10 seconds")
		}
	})
}

func (s *service) disconnect() {
	if s.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if s.session != nil {
		s.session.EndSession(ctx)
	}
	if err := s.client.Disconnect(ctx); err != nil {
		log.Error().Err(databaseError("disconnect dedicated client", err)).Msg("Subscription snapshot disconnect failed")
	}
}
