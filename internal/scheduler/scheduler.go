package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/msjurset/goback/internal/backup"
	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

type Scheduler struct {
	cron  *cron.Cron
	store *storage.Manager
}

func New(store *storage.Manager) *Scheduler {
	return &Scheduler{
		cron:  cron.New(),
		store: store,
	}
}

func (s *Scheduler) Add(cfg config.BackupConfig) error {
	c := cfg // capture for closure
	_, err := s.cron.AddFunc(c.Schedule, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		if err := backup.Execute(ctx, c, s.store, backup.Options{}); err != nil {
			log.Printf("error: backup %s failed: %v", c.Name, err)
			_ = s.store.RecordBackup(storage.BackupRecord{
				Name:      c.Name,
				Timestamp: time.Now(),
				Error:     err.Error(),
			})
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
