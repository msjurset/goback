package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/msjurseth/markback/internal/backup"
	"github.com/msjurseth/markback/internal/config"
	"github.com/msjurseth/markback/internal/credentials"
	"github.com/msjurseth/markback/internal/daemon"
	"github.com/msjurseth/markback/internal/storage"
)

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("markback: ")

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "init":
		cmdInit()
		return
	case "auth":
		cmdAuth()
		return
	case "version", "-v", "--version":
		fmt.Printf("markback %s\n", version)
		return
	case "help", "-h", "--help":
		usage()
		return
	}

	cfg := loadConfig()
	setupLogging(cfg.Storage.LogFile)

	switch os.Args[1] {
	case "daemon":
		cmdDaemon(cfg)
	case "run":
		name := ""
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		cmdRun(cfg, name, false)
	case "now":
		cmdRun(cfg, "", false)
	case "dry-run":
		name := ""
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		cmdRun(cfg, name, true)
	case "list":
		cmdList(cfg)
	case "status":
		cmdStatus(cfg)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: markback <command> [args]

Commands:
  init              Create default config file
  auth              Resolve and cache op:// secrets in system keychain
  auth --clear      Remove cached secrets from system keychain
  daemon            Run the backup scheduler (foreground)
  run [name]        Manually trigger one or all backups
  now               Run all backups immediately
  dry-run [name]    Simulate backups (connect but don't transfer)
  list              Show configured backup jobs
  status            Show recent backup history

Service management (macOS):
  launchctl load ~/Library/LaunchAgents/com.markback.daemon.plist
  launchctl unload ~/Library/LaunchAgents/com.markback.daemon.plist
`)
}

func loadConfig() *config.Config {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	return cfg
}

func setupLogging(logFile string) {
	if logFile == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		log.Printf("warning: could not create log directory: %v", err)
		return
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("warning: could not open log file: %v", err)
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
}

func cmdInit() {
	path := config.DefaultPath()
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("config already exists: %s\n", path)
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Fatalf("creating config directory: %v", err)
	}

	if err := os.WriteFile(path, []byte(config.DefaultConfig()), 0o644); err != nil {
		log.Fatalf("writing config: %v", err)
	}

	fmt.Printf("created config: %s\n", path)
	fmt.Println("edit the file to configure your backup targets")
}

func cmdAuth() {
	clear := len(os.Args) > 2 && os.Args[2] == "--clear"

	cfg, err := config.LoadRaw(config.DefaultPath())
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	type secret struct {
		key   string
		opRef string
		label string
	}

	var secrets []secret
	for _, b := range cfg.Backups {
		if strings.HasPrefix(b.HAToken, "op://") {
			secrets = append(secrets, secret{
				key:   "ha_token_" + b.Name,
				opRef: b.HAToken,
				label: b.Name + " (ha_token)",
			})
		}
		if strings.HasPrefix(b.SSHKey, "op://") {
			secrets = append(secrets, secret{
				key:   "ssh_key_" + b.Name,
				opRef: b.SSHKey,
				label: b.Name + " (ssh_key)",
			})
		}
	}

	if len(secrets) == 0 {
		fmt.Println("no op:// secrets found in config")
		return
	}

	for _, s := range secrets {
		if clear {
			if err := credentials.Delete(s.key); err != nil {
				log.Printf("error clearing %s: %v", s.label, err)
			} else {
				fmt.Printf("cleared %s from keychain\n", s.label)
			}
			continue
		}

		fmt.Printf("resolving %s ...\n", s.label)
		if _, err := credentials.ResolveAndCache(s.key, s.opRef); err != nil {
			log.Printf("error: %s: %v", s.label, err)
		} else {
			fmt.Printf("cached %s in system keychain\n", s.label)
		}
	}
}

func cmdDaemon(cfg *config.Config) {
	if err := daemon.Run(cfg); err != nil {
		log.Fatalf("daemon error: %v", err)
	}
}

func cmdRun(cfg *config.Config, name string, dryRun bool) {
	store := storage.New(cfg.Storage.BaseDir)
	opts := backup.Options{DryRun: dryRun}

	var targets []config.BackupConfig
	if name == "" {
		targets = cfg.Backups
	} else {
		for _, b := range cfg.Backups {
			if b.Name == name {
				targets = append(targets, b)
				break
			}
		}
		if len(targets) == 0 {
			log.Fatalf("no backup named %q found in config", name)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	type result struct {
		name   string
		status string
		err    error
	}
	var results []result

	for _, t := range targets {
		err := backup.Execute(ctx, t, store, opts)
		r := result{name: t.Name}
		if err != nil {
			r.status = "FAILED"
			r.err = err
			if !dryRun {
				_ = store.RecordBackup(storage.BackupRecord{
					Name:      t.Name,
					Timestamp: time.Now(),
					Error:     err.Error(),
				})
			}
		} else {
			r.status = "OK"
		}
		results = append(results, r)
	}

	if dryRun {
		fmt.Println()
		fmt.Println("=== Dry Run Summary ===")
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tTYPE\tSTATUS\tDETAIL")
		for i, r := range results {
			detail := ""
			if r.err != nil {
				detail = r.err.Error()
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", targets[i].Name, targets[i].Type, r.status, detail)
		}
		w.Flush()
	}

	for _, r := range results {
		if r.err != nil {
			os.Exit(1)
		}
	}
}

func cmdList(cfg *config.Config) {

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tFOLDER\tSCHEDULE\tRETENTION")
	for _, b := range cfg.Backups {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n", b.Name, b.Type, b.Folder, b.Schedule, b.Retention)
	}
	w.Flush()
}

func cmdStatus(cfg *config.Config) {
	store := storage.New(cfg.Storage.BaseDir)

	sl, err := store.LoadLog()
	if err != nil {
		log.Fatalf("loading status: %v", err)
	}

	if len(sl.Records) == 0 {
		fmt.Println("no backups recorded yet")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTIMESTAMP\tFILE\tSIZE\tSTATUS")

	// Show most recent first, limit to last 20
	start := 0
	if len(sl.Records) > 20 {
		start = len(sl.Records) - 20
	}
	for i := len(sl.Records) - 1; i >= start; i-- {
		r := sl.Records[i]
		status := "ok"
		if r.Error != "" {
			status = "FAILED: " + r.Error
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			r.Name,
			r.Timestamp.Format("2006-01-02 15:04:05"),
			r.Filename,
			formatSize(r.Size),
			status,
		)
	}
	w.Flush()
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
