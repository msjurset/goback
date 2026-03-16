package scheduler

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/msjurseth/markback/internal/backup"
	"github.com/msjurseth/markback/internal/config"
	"github.com/msjurseth/markback/internal/storage"
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
