package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/msjurset/goback/internal/backup"
	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/runlock"
	"github.com/msjurset/goback/internal/storage"
)

type Scheduler struct {
	cron  *cron.Cron
	store *storage.Manager
	coord *runlock.Coordinator
}

func New(store *storage.Manager, coord *runlock.Coordinator) *Scheduler {
	return &Scheduler{
		cron:  cron.New(),
		store: store,
		coord: coord,
	}
}

func (s *Scheduler) Add(cfg config.BackupConfig) error {
	c := cfg // capture for closure
	_, err := s.cron.AddFunc(c.Schedule, func() {
		release, ok := s.coord.TryLock(c.Name)
		if !ok {
			log.Printf("scheduler: %s already running, skipping this fire", c.Name)
			return
		}
		defer release()

		// 60-minute outer cap accommodates a full retry envelope under the
		// default policy (5 attempts × 15-minute max backoff is comfortably
		// inside this) on top of the actual transfer time.
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
		defer cancel()

		// backup.Execute records each attempt (including final failure) to
		// status.json itself, so we don't double-record here.
		if err := backup.Execute(ctx, c, s.store, backup.Options{}); err != nil {
			log.Printf("error: backup %s failed after retries: %v", c.Name, err)
		}
	})
	if err != nil {
		return err
	}
	log.Printf("scheduled backup: %s (%s)", cfg.Name, cfg.Schedule)
	return nil
}

func (s *Scheduler) Start() {
	s.cron.Start()
}

func (s *Scheduler) Stop() context.Context {
	return s.cron.Stop()
}

// ReplaceAll stops the current cron, rebuilds with new configs, and restarts.
func (s *Scheduler) ReplaceAll(backups []config.BackupConfig) {
	// Stop current scheduler and wait for running jobs
	stopCtx := s.cron.Stop()
	<-stopCtx.Done()

	// Create new cron instance
	s.cron = cron.New()

	for _, b := range backups {
		if err := s.Add(b); err != nil {
			log.Printf("error: schedule %s: %v", b.Name, err)
		}
	}

	s.cron.Start()
}
