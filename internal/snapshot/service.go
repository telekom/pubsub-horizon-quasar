// Copyright 2026 Deutsche Telekom AG
//
// SPDX-License-Identifier: Apache-2.0

package snapshot

import (
	"context"
	"errors"
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

type workCause uint8

const (
	refreshWork workCause = iota
	deadlineWork
	connectionWork
	publicationWork
)

type service struct {
	config            config.SubscriptionSnapshots
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	stopOnce          sync.Once
	client            *mongo.Client
	session           mongo.Session
	store             *mongoStore
	worker            *worker
	zooKeeper         zooKeeperTransport
	mongoRetryBlocked bool
	refreshRequested  bool

	initialRefreshDeadline time.Time
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
		Str("refreshInterval", c.RefreshInterval.String()).
		Str("initialRefreshDelay", c.InitialRefreshDelay.String()).
		Int("minimumRetainedSnapshots", c.MinimumRetainedSnapshots).
		Int64("maxSnapshotBytes", c.MaxSnapshotBytes).
		Str("refreshTimeout", c.RefreshTimeout.String()).
		Str("cleanupTimeout", c.CleanupTimeout.String()).
		Str("activationDelay", c.ActivationDelay.String()).
		Str("zookeeperBasePath", c.ZooKeeper.BasePath).
		Str("zookeeperClient", "Shopify/zk").
		Msg("Starting subscription snapshot worker")
}

func newService(c config.SubscriptionSnapshots) *service {
	ctx, cancel := context.WithCancel(context.Background())
	return &service{config: c, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (s *service) run() {
	defer close(s.done)
	defer s.disconnect()
	s.zooKeeper = newZooKeeperClient(s.ctx, s.config.ZooKeeper, s.config.RefreshInterval)
	refresh := time.NewTicker(s.config.RefreshInterval)
	defer refresh.Stop()
	s.loop(refresh.C, nil)
}

func (s *service) loop(refresh <-chan time.Time, actions <-chan func()) {
	if s.config.InitialRefreshDelay > 0 {
		s.initialRefreshDeadline = time.Now().Add(s.config.InitialRefreshDelay)
	}
	s.attemptRefresh()
	recoveryWakeUsed := false
	for {
		var wake <-chan struct{}
		var timer *time.Timer
		var activation <-chan time.Time
		if s.zooKeeper != nil {
			wake = s.zooKeeper.events()
		}
		if deadline := s.nextDeadline(); !deadline.IsZero() {
			timer = time.NewTimer(max(time.Until(deadline), 0))
			activation = timer.C
		}
		select {
		case <-s.ctx.Done():
			stopActivationTimer(timer)
			return
		case <-refresh:
			stopActivationTimer(timer)
			runLoopActions(actions)
			recoveryWakeUsed = false
			s.attemptPeriodicRefresh()
		case <-activation:
			runLoopActions(actions)
			if s.attemptDeadline(refresh) {
				recoveryWakeUsed = false
			}
		case <-wake:
			stopActivationTimer(timer)
			runLoopActions(actions)
			recoveryWakeUsed = s.connectionWake(recoveryWakeUsed)
		case action := <-actions:
			stopActivationTimer(timer)
			action()
		}
	}
}

func (s *service) nextDeadline() time.Time {
	deadline := s.initialRefreshDeadline
	if s.worker != nil && s.worker.publication != nil {
		p := s.worker.publication
		if next := p.nextDeadline(s.mongoRetryBlocked); !next.IsZero() && (deadline.IsZero() || next.Before(deadline)) {
			deadline = next
		}
	}
	return deadline
}

func (s *service) attemptPeriodicRefresh() {
	s.finishInitialRefreshDelay()
	s.mongoRetryBlocked = false
	if s.worker != nil && s.worker.publication != nil {
		s.worker.publication.allowRetry()
	}
	s.attemptRefresh()
}

func (s *service) attemptDeadline(refresh <-chan time.Time) bool {
	if !s.finishInitialRefreshDelay() {
		s.runCycle(deadlineWork)
		return false
	}
	select {
	case <-refresh:
		s.attemptPeriodicRefresh()
		return true
	default:
	}
	if s.worker != nil && s.worker.publication != nil {
		s.worker.publication.allowRetry()
	}
	s.attemptRefresh()
	return false
}

func runLoopActions(actions <-chan func()) {
	for {
		select {
		case action := <-actions:
			action()
		default:
			return
		}
	}
}

func (s *service) connectionWake(recoveryWakeUsed bool) bool {
	if s.worker == nil || s.worker.publication == nil || s.initialRefreshWaiting() {
		return recoveryWakeUsed
	}
	switch {
	case !s.zooKeeper.available():
		recoveryWakeUsed = false
		s.worker.publication.degrade(zooKeeperError("session", errZooKeeperUnavailable))
	case recoveryWakeUsed:
		return recoveryWakeUsed
	default:
		recoveryWakeUsed = true
		s.worker.publication.allowRetry()
	}
	s.runCycle(connectionWork)
	return recoveryWakeUsed
}

func (s *service) initialRefreshWaiting() bool {
	return !s.initialRefreshDeadline.IsZero() && time.Now().Before(s.initialRefreshDeadline)
}

func (s *service) finishInitialRefreshDelay() bool {
	if s.initialRefreshDeadline.IsZero() || time.Now().Before(s.initialRefreshDeadline) {
		return false
	}
	s.initialRefreshDeadline = time.Time{}
	return true
}

func stopActivationTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
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
	if s.zooKeeper != nil && s.worker.publication == nil {
		s.worker.publication = newZooKeeperPublication(s.zooKeeper, s.config.ActivationDelay)
	}
	return nil
}

func (s *service) connect(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	opts := options.Client().ApplyURI(s.config.URI).
		SetReadPreference(readpref.Primary())
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
	s.runCycle(refreshWork)
}

func (s *service) runCycle(cause workCause) {
	refresh := cause == refreshWork
	cleanupEligible := cause == refreshWork || cause == deadlineWork
	for {
		var start time.Time
		var err error
		if refresh {
			if s.worker != nil && s.worker.proposal != nil && s.worker.publication != nil {
				s.refreshRequested = true
			} else {
				start, err = s.refreshStep()
				if start.IsZero() {
					return
				}
				cleanupEligible = true
			}
		}
		s.publicationStep()
		if !start.IsZero() {
			s.logRefresh(start, err)
		}
		if s.worker == nil || s.worker.proposal != nil || !s.refreshRequested || s.ctx.Err() != nil {
			break
		}
		s.refreshRequested = false
		refresh = true
	}
	if cleanupEligible || cause == connectionWork && s.zooKeeper.available() {
		s.cleanupAfterPublication()
	}
}

func (s *service) refreshStep() (time.Time, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.config.RefreshTimeout)
	defer cancel()
	start := time.Now()
	err := s.initialize(ctx)
	if err == nil && s.initialRefreshWaiting() {
		log.Debug().Time("notBefore", s.initialRefreshDeadline).
			Msg("Subscription snapshot initial refresh waiting for startup delay")
		return time.Time{}, nil
	}
	if err == nil {
		s.finishInitialRefreshDelay()
		if s.worker.proposal == nil {
			err = s.withSession(ctx, s.worker.createSnapshot)
		}
		if err == nil && s.worker.publication == nil && s.worker.proposal != nil {
			err = s.withSession(ctx, s.worker.resolve)
		}
	}
	return start, err
}

func (s *service) logRefresh(start time.Time, err error) {
	if err != nil {
		s.logError("refresh", start, err)
		return
	}
	log.Info().Dur("durationMs", time.Since(start)).Str("sourceCollection", s.config.SourceCollection).
		Str("snapshotCollection", s.config.SnapshotCollection).Str("headCollection", s.config.HeadCollection).
		Time("lastSuccess", s.worker.lastSuccess).Msg("Subscription snapshot refresh completed")
}

func (s *service) publicationStep() {
	if s.worker == nil {
		return
	}
	p := s.worker.publication
	if p == nil || s.ctx.Err() != nil || s.initialRefreshWaiting() {
		return
	}
	if p.canRetry() {
		s.attemptZooKeeper()
	}
	mongoPublished := false
	if s.worker.proposal != nil && p.mongoAllowed(s.worker.proposedSnapshot()) && !s.mongoRetryBlocked {
		ctx, cancel := context.WithTimeout(s.ctx, s.config.RefreshTimeout)
		start := time.Now()
		err := s.withSession(ctx, s.worker.resolve)
		cancel()
		if err != nil {
			s.mongoRetryBlocked = true
			p.blockRetry()
			s.logError("activate-mongo", start, err)
			return
		}
		mongoPublished = true
	}
	if mongoPublished && p.needsPreparation() {
		s.attemptZooKeeper()
	}
	if p.activationDue() {
		s.attemptZooKeeper()
	}
}

func (s *service) attemptZooKeeper() {
	ctx, cancel := context.WithTimeout(s.ctx, min(zooKeeperIOTimeout, s.config.RefreshTimeout))
	start := time.Now()
	err := s.withSession(ctx, s.worker.progressZooKeeper)
	cancel()
	if err == nil {
		return
	}
	p := s.worker.publication
	p.degrade(err)
	id := p.snapshotID(s.worker.proposedSnapshot())
	category := zooKeeperErrorCategory(err)
	var operationError *zooKeeperOperationError
	if errors.As(err, &operationError) {
		category = operationError.category
	}
	log.Error().Err(err).Str("operation", "zookeeper").Str("phase", p.phase()).
		Str("snapshotId", id).Str("errorCategory", category).Dur("durationMs", time.Since(start)).
		Msg("Subscription snapshot ZooKeeper operation failed")
}

func (s *service) cleanupAfterPublication() {
	if s.worker != nil && s.worker.cleanupDue && s.ctx.Err() == nil {
		s.attemptCleanup()
	}
}

func (s *service) attemptCleanup() {
	ctx, cancel := context.WithTimeout(s.ctx, s.config.CleanupTimeout)
	start := time.Now()
	err := s.withSession(ctx, s.worker.cleanupPending)
	cancel()
	if err != nil {
		if errors.Is(err, errCleanupDeferred) {
			log.Warn().Err(err).Str("operation", "cleanup").Dur("durationMs", time.Since(start)).
				Msg("Subscription snapshot cleanup deferred")
			return
		}
		s.logError("cleanup", start, err)
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if s.zooKeeper != nil {
		if err := s.zooKeeper.close(ctx); err != nil {
			log.Error().Err(err).Msg("Subscription snapshot ZooKeeper disconnect failed")
		}
	}
	if s.client == nil {
		return
	}
	if s.session != nil {
		s.session.EndSession(ctx)
	}
	if err := s.client.Disconnect(ctx); err != nil {
		log.Error().Err(databaseError("disconnect dedicated client", err)).Msg("Subscription snapshot disconnect failed")
	}
}
