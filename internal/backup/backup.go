package backup

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/retry"
	"github.com/msjurset/goback/internal/storage"
)

type Options struct {
	DryRun bool
	// NoRetry forces a single attempt with no backoff. Used by interactive
	// CLI commands (`goback run`, `goback now`) where retry would surprise
	// the user; the daemon path always retries per the configured policy.
	NoRetry bool
	// MaxAttempts, when > 0, overrides retry.Default.MaxAttempts and any
	// per-backup RetryMaxAttempts. Set by `goback run --retries N` to opt
	// the CLI/control-socket path into retries. NoRetry still wins if set.
	MaxAttempts int
}

// Result is what a Provider returns on a successful run. The fields are used
// by Execute to record the success in status.json.
type Result struct {
	Filename string
	Size     int64
}

type Provider interface {
	Run(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) (Result, error)
}

func NewProvider(backupType string) (Provider, error) {
	switch backupType {
	case "ha_api":
		return &HAProvider{}, nil
	case "ssh":
		return &SSHProvider{}, nil
	case "local":
		return &LocalProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown backup type: %s", backupType)
	}
}

// Execute runs cfg through its provider with retry/backoff. Every failed
// attempt is logged to status.json (with Attempt set); the final successful
// attempt records a success row. A retry.Permanent error from a provider
// short-circuits the loop. Dry runs skip retry entirely.
func Execute(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) error {
	provider, err := NewProvider(cfg.Type)
	if err != nil {
		return err
	}

	prefix := ""
	if opts.DryRun {
		prefix = "[dry-run] "
	}
	log.Printf("%sstarting backup: %s (type: %s)", prefix, cfg.Name, cfg.Type)

	if !opts.DryRun {
		if err := store.EnsureDir(cfg.Folder); err != nil {
			return fmt.Errorf("creating backup directory: %w", err)
		}
	}

	if opts.DryRun {
		if _, err := provider.Run(ctx, cfg, store, opts); err != nil {
			return err
		}
		log.Printf("%sbackup complete: %s", prefix, cfg.Name)
		return nil
	}

	policy := retry.Default
	if cfg.RetryMaxAttempts > 0 {
		policy.MaxAttempts = cfg.RetryMaxAttempts
	}
	if opts.MaxAttempts > 0 {
		policy.MaxAttempts = opts.MaxAttempts
	}
	if opts.NoRetry {
		policy.MaxAttempts = 1
	}

	hook := func(attempt int, err error, next time.Duration) {
		_ = store.RecordBackup(storage.BackupRecord{
			Name:      cfg.Name,
			Timestamp: time.Now(),
			Error:     err.Error(),
			Attempt:   attempt,
		})
		if next > 0 {
			log.Printf("[%s] attempt %d failed: %v (retrying in %s)", cfg.Name, attempt, err, next.Round(time.Second))
		} else {
			log.Printf("[%s] attempt %d failed: %v (giving up)", cfg.Name, attempt, err)
		}
	}

	var result Result
	var finalAttempt int
	runErr := retry.Do(ctx, policy, hook, func(attempt int) error {
		finalAttempt = attempt
		r, err := provider.Run(ctx, cfg, store, opts)
		if err != nil {
			return err
		}
		result = r
		return nil
	})
	if runErr != nil {
		// NoRetry runs are interactive — the user is staring at the error.
		// Daemon-driven runs fire the notify hook so failures don't get lost
		// in the log when nobody's watching.
		if !opts.NoRetry {
			notifyFailure(cfg, runErr, finalAttempt)
		}
		return runErr
	}

	if err := store.RecordBackup(storage.BackupRecord{
		Name:      cfg.Name,
		Filename:  result.Filename,
		Timestamp: time.Now(),
		Size:      result.Size,
		Attempt:   finalAttempt,
	}); err != nil {
		log.Printf("warning: recording success for %s: %v", cfg.Name, err)
	}

	if err := store.EnforceRetention(cfg.Folder, cfg.Retention); err != nil {
		log.Printf("warning: retention cleanup failed for %s: %v", cfg.Name, err)
	}

	log.Printf("%sbackup complete: %s (attempt %d)", prefix, cfg.Name, finalAttempt)
	return nil
}
