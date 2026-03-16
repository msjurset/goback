package backup

import (
	"context"
	"fmt"
	"log"

	"github.com/msjurseth/markback/internal/config"
	"github.com/msjurseth/markback/internal/storage"
)

type Options struct {
	DryRun bool
}

type Provider interface {
	Run(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) error
}

func NewProvider(backupType string) (Provider, error) {
	switch backupType {
	case "ha_api":
		return &HAProvider{}, nil
	case "ssh":
		return &SSHProvider{}, nil
	default:
		return nil, fmt.Errorf("unknown backup type: %s", backupType)
	}
}

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

	if err := provider.Run(ctx, cfg, store, opts); err != nil {
		return err
	}

	if !opts.DryRun {
		if err := store.EnforceRetention(cfg.Folder, cfg.Retention); err != nil {
			log.Printf("warning: retention cleanup failed for %s: %v", cfg.Name, err)
		}
	}

	log.Printf("%sbackup complete: %s", prefix, cfg.Name)
	return nil
}
