package backup

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

type LocalProvider struct{}

func (p *LocalProvider) Run(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) error {
	if opts.DryRun {
		return p.dryRun(ctx, cfg)
	}

	if cfg.PreCommand != "" {
		log.Printf("[%s] running pre_command: %s", cfg.Name, cfg.PreCommand)
		if err := p.runCommand(ctx, cfg.PreCommand); err != nil {
			return fmt.Errorf("pre_command: %w", err)
		}
	}

	localPath := cfg.LocalPath
	if localPath == "" && cfg.LocalPattern != "" {
		resolved, err := p.resolvePattern(cfg.LocalPattern)
		if err != nil {
			return fmt.Errorf("resolving local_pattern: %w", err)
		}
		log.Printf("[%s] local_pattern matched: %s", cfg.Name, resolved)
		localPath = resolved
	}

	if localPath == "" {
		return fmt.Errorf("no file to back up: local_path or local_pattern must resolve to a file")
	}

	ext := compoundExt(localPath)
	if ext == "" {
		ext = ".backup"
	}
	dest := store.BackupPath(cfg.Folder, cfg.Name, ext, cfg.Filename)

	log.Printf("[%s] copying %s -> %s", cfg.Name, localPath, dest)
	size, err := p.copyFile(localPath, dest)
	if err != nil {
		return fmt.Errorf("copy file: %w", err)
	}

	if cfg.PostCommand != "" {
		log.Printf("[%s] running post_command: %s", cfg.Name, cfg.PostCommand)
		if err := p.runCommand(ctx, cfg.PostCommand); err != nil {
			log.Printf("[%s] warning: post_command failed: %v", cfg.Name, err)
		}
	}

	return store.RecordBackup(storage.BackupRecord{
		Name:      cfg.Name,
		Filename:  filepath.Base(dest),
		Timestamp: time.Now(),
		Size:      size,
	})
}

func (p *LocalProvider) dryRun(ctx context.Context, cfg config.BackupConfig) error {
	if cfg.LocalPattern != "" {
		resolved, err := p.resolvePattern(cfg.LocalPattern)
		if err != nil {
			if cfg.PreCommand != "" {
				log.Printf("[%s] local_pattern %s: no match yet (file may be created by pre_command)", cfg.Name, cfg.LocalPattern)
			} else {
				return fmt.Errorf("local_pattern no match: %s", cfg.LocalPattern)
			}
		} else {
			log.Printf("[%s] local_pattern matched: %s", cfg.Name, resolved)
		}
	} else if cfg.LocalPath != "" {
		info, err := os.Stat(cfg.LocalPath)
		if err != nil {
			if cfg.PreCommand != "" {
				log.Printf("[%s] local file %s: not found (file may be created by pre_command)", cfg.Name, cfg.LocalPath)
			} else {
				return fmt.Errorf("local file not found: %s", cfg.LocalPath)
			}
		} else {
			log.Printf("[%s] local file %s: exists (%d bytes)", cfg.Name, cfg.LocalPath, info.Size())
		}
	}

	if cfg.PreCommand != "" {
		log.Printf("[%s] pre_command configured: %s (skipped)", cfg.Name, cfg.PreCommand)
	}
	if cfg.PostCommand != "" {
		log.Printf("[%s] post_command configured: %s (skipped)", cfg.Name, cfg.PostCommand)
	}

	return nil
}

func (p *LocalProvider) runCommand(ctx context.Context, command string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (p *LocalProvider) resolvePattern(pattern string) (string, error) {
	// Expand ~ in pattern
	if strings.HasPrefix(pattern, "~/") {
		home, _ := os.UserHomeDir()
		pattern = filepath.Join(home, pattern[2:])
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid glob pattern: %w", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no files matching pattern %s", pattern)
	}

	// Sort by modification time, newest first
	sort.Slice(matches, func(i, j int) bool {
		infoI, _ := os.Stat(matches[i])
		infoJ, _ := os.Stat(matches[j])
		if infoI == nil || infoJ == nil {
			return false
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})

	return matches[0], nil
}

func (p *LocalProvider) copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	n, err := io.Copy(out, in)
	if err != nil {
		return 0, err
	}

	return n, out.Close()
}
