package catchup

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

const (
	checkInterval = 5 * time.Minute

	// Catchup lookback bounds. Floor of 7 days ensures we always cover at
	// least a weekly schedule even if cycle estimation is off; ceiling of
	// 90 days bounds the cron walk for very long schedules (yearly).
	minLookback = 7 * 24 * time.Hour
	maxLookback = 90 * 24 * time.Hour
)

// scheduleCycle estimates the longest gap between consecutive fire times by
// sampling several future fires from now. Used to size the catchup lookback
// window so monthly/quarterly schedules are detectable.
func scheduleCycle(expr string, now time.Time) (time.Duration, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return 0, err
	}
	var maxGap time.Duration
	cursor := now
	for range 5 {
		next := sched.Next(cursor)
		gap := next.Sub(cursor)
		if gap > maxGap {
			maxGap = gap
		}
		cursor = next
	}
	return maxGap, nil
}

// PrevFireTime returns the most recent scheduled fire time for expr at or
// before now, walking forward from now-lookback. Returns zero time if no fire
// time falls inside the window.
func PrevFireTime(expr string, now time.Time, lookback time.Duration) (time.Time, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, err
	}

	cursor := now.Add(-lookback)
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
// backup for name. Returns zero time if there is no successful record.
func LastSuccessfulBackup(statusLog *storage.StatusLog, name string) time.Time {
	var latest time.Time
	for _, r := range statusLog.Records {
		if r.Name == name && r.Error == "" && r.Timestamp.After(latest) {
			latest = r.Timestamp
		}
	}
	return latest
}

// LastAttempt returns the timestamp of the most recent attempt for name —
// success or failure. Returns zero time if there are no records.
//
// FindMissed uses this (not LastSuccessfulBackup) so a permanently-failing
// backup with retry-exhausted error records doesn't get re-fired by catchup
// every 5 minutes; instead it waits for the next scheduled fire to elapse.
func LastAttempt(statusLog *storage.StatusLog, name string) time.Time {
	var latest time.Time
	for _, r := range statusLog.Records {
		if r.Name == name && r.Timestamp.After(latest) {
			latest = r.Timestamp
		}
	}
	return latest
}

// FindMissed returns backup configs whose most recent scheduled fire time
// (within a schedule-cycle-derived lookback) is after their last attempt.
//
// daemonStart is used as the baseline for backups that have never been
// attempted in this status log: only fires that occurred while the daemon
// was up are considered missed. This catches the "added a backup, daemon
// is starting, schedule fired during downtime" case while not retroactively
// running history for a brand-new backup.
func FindMissed(backups []config.BackupConfig, statusLog *storage.StatusLog, daemonStart, now time.Time) []config.BackupConfig {
	var missed []config.BackupConfig
	for _, b := range backups {
		cycle, err := scheduleCycle(b.Schedule, now)
		if err != nil {
			log.Printf("catchup: bad schedule for %s: %v", b.Name, err)
			continue
		}
		lookback := 2 * cycle
		if lookback < minLookback {
			lookback = minLookback
		}
		if lookback > maxLookback {
			lookback = maxLookback
		}

		prev, err := PrevFireTime(b.Schedule, now, lookback)
		if err != nil {
			log.Printf("catchup: bad schedule for %s: %v", b.Name, err)
			continue
		}
		if prev.IsZero() {
			continue
		}

		last := LastAttempt(statusLog, b.Name)
		if last.IsZero() {
			last = daemonStart
		}
		if last.Before(prev) {
			missed = append(missed, b)
		}
	}
	return missed
}

// Checker periodically checks for missed backups and runs them.
type Checker struct {
	backups     []config.BackupConfig
	store       *storage.Manager
	coord       *runlock.Coordinator
	daemonStart time.Time
}

// NewChecker creates a Checker for the given backups, storage manager, and
// run coordinator. The coordinator must be the same instance the scheduler
// uses, so a cron-fired job and a catchup-fired job for the same backup
// name don't run simultaneously. daemonStart is the baseline for never-run
// backups (see FindMissed).
func NewChecker(backups []config.BackupConfig, store *storage.Manager, coord *runlock.Coordinator) *Checker {
	return &Checker{
		backups:     backups,
		store:       store,
		coord:       coord,
		daemonStart: time.Now(),
	}
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

	missed := FindMissed(c.backups, statusLog, c.daemonStart, time.Now())
	for _, b := range missed {
		if ctx.Err() != nil {
			return
		}

		release, ok := c.coord.TryLock(b.Name)
		if !ok {
			// already executing via the cron path; let that run finish
			continue
		}

		log.Printf("catchup: running missed backup: %s", b.Name)

		runCtx, cancel := context.WithTimeout(ctx, 60*time.Minute)
		// backup.Execute records each attempt's outcome internally.
		if err := backup.Execute(runCtx, b, c.store, backup.Options{}); err != nil {
			log.Printf("catchup: backup %s failed after retries: %v", b.Name, err)
		}
		cancel()
		release()
	}
}
