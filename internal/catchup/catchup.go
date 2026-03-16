package catchup

import (
	"context"
	"log"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/msjurset/goback/internal/backup"
	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

const checkInterval = 5 * time.Minute

// PrevFireTime returns the most recent scheduled fire time for the given cron
// expression relative to now. It walks Schedule.Next from 7 days ago until
// passing now, returning the last time before now. Returns zero time if no
// fire time exists in the window.
func PrevFireTime(expr string, now time.Time) (time.Time, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, err
	}

	cursor := now.Add(-7 * 24 * time.Hour)
	var prev time.Time
	for {
		next := sched.Next(cursor)
		if next.After(now) {
			break
		}
		prev = next
		cursor = next
	}
	return prev, nil
}

// LastSuccessfulBackup returns the timestamp of the most recent successful
// backup for the given name from the status log. Returns zero time if none found.
func LastSuccessfulBackup(statusLog *storage.StatusLog, name string) time.Time {
	var latest time.Time
	for _, r := range statusLog.Records {
		if r.Name == name && r.Error == "" && r.Timestamp.After(latest) {
			latest = r.Timestamp
		}
	}
	return latest
}

// FindMissed returns backup configs that have a missed scheduled run. A backup
// is considered missed if its most recent scheduled fire time is after its last
// successful backup. Backups that have never run are skipped.
func FindMissed(backups []config.BackupConfig, statusLog *storage.StatusLog, now time.Time) []config.BackupConfig {
	var missed []config.BackupConfig
	for _, b := range backups {
		prev, err := PrevFireTime(b.Schedule, now)
		if err != nil {
			log.Printf("catchup: bad schedule for %s: %v", b.Name, err)
			continue
		}
		if prev.IsZero() {
			continue
		}

		last := LastSuccessfulBackup(statusLog, b.Name)
		if last.IsZero() {
			// Never run — skip, user should use "goback run"
			continue
		}
		if last.Before(prev) {
			missed = append(missed, b)
		}
	}
	return missed
}

// Checker periodically checks for missed backups and runs them.
type Checker struct {
	backups []config.BackupConfig
	store   *storage.Manager
}

// NewChecker creates a Checker for the given backups and storage manager.
func NewChecker(backups []config.BackupConfig, store *storage.Manager) *Checker {
	return &Checker{backups: backups, store: store}
}

// Run checks for and runs missed backups immediately, then re-checks every
// 5 minutes. It blocks until ctx is cancelled.
func (c *Checker) Run(ctx context.Context) {
	c.checkAndRun(ctx)

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkAndRun(ctx)
		}
	}
}

func (c *Checker) checkAndRun(ctx context.Context) {
	statusLog, err := c.store.LoadLog()
	if err != nil {
		log.Printf("catchup: error loading status log: %v", err)
		return
	}

	missed := FindMissed(c.backups, statusLog, time.Now())
	for _, b := range missed {
		if ctx.Err() != nil {
			return
		}
		log.Printf("catchup: running missed backup: %s", b.Name)

		runCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		if err := backup.Execute(runCtx, b, c.store, backup.Options{}); err != nil {
			log.Printf("catchup: backup %s failed: %v", b.Name, err)
			_ = c.store.RecordBackup(storage.BackupRecord{
				Name:      b.Name,
				Timestamp: time.Now(),
				Error:     err.Error(),
			})
		}
		cancel()
	}
}
