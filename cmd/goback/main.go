package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/msjurset/goback/internal/backup"
	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/control"
	"github.com/msjurset/goback/internal/credentials"
	"github.com/msjurset/goback/internal/daemon"
)

var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("goback: ")

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
	case "clear":
		name := ""
		if len(os.Args) > 2 {
			name = os.Args[2]
		}
		cmdClear(name)
		return
	case "completion":
		cmdCompletion()
		return
	case "version", "-v", "--version":
		fmt.Printf("goback %s\n", version)
		return
	case "help", "-h", "--help":
		usage()
		return
	}

	switch os.Args[1] {
	case "daemon":
		cfg := loadConfig()
		logFile := setupLogging(cfg.Storage.LogFile)
		tee := control.NewLogTee()
		// Append the tee to whatever setupLogging installed (stderr, or
		// stderr+file). Subscribers register via tee.Add for the duration of
		// a request and unregister on completion.
		if logFile != nil {
			log.SetOutput(io.MultiWriter(os.Stderr, logFile, tee))
		} else {
			log.SetOutput(io.MultiWriter(os.Stderr, tee))
		}
		cmdDaemon(cfg, tee)
	case "run":
		name, local := parseRunArgs(os.Args[2:])
		dispatchRun(name, false, local)
	case "now":
		_, local := parseRunArgs(os.Args[2:])
		dispatchRun("", false, local)
	case "dry-run":
		name, local := parseRunArgs(os.Args[2:])
		dispatchRun(name, true, local)
	case "list":
		cfg := loadConfig()
		setupLogging(cfg.Storage.LogFile)
		cmdList(cfg)
	case "status":
		cfg := loadConfig()
		setupLogging(cfg.Storage.LogFile)
		cmdStatus(cfg)
	case "last":
		cfg := loadConfig()
		setupLogging(cfg.Storage.LogFile)
		name := ""
		epoch := false
		for _, a := range os.Args[2:] {
			if a == "--epoch" || a == "-e" {
				epoch = true
				continue
			}
			if name == "" {
				name = a
			}
		}
		cmdLast(cfg, name, epoch)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

// parseRunArgs separates the --local flag from a positional backup name.
// Unknown args are treated as the name (first one wins).
func parseRunArgs(args []string) (name string, local bool) {
	for _, a := range args {
		switch a {
		case "--local":
			local = true
		default:
			if name == "" {
				name = a
			}
		}
	}
	return
}

// dispatchRun is the entry point for run/now/dry-run. By default it routes
// the request to the running daemon over its control socket — the daemon has
// keychain access in the user's GUI session, which cron- and launchd-spawned
// invocations of `goback run` do not. --local forces in-process execution
// (used for debugging or when no daemon is running).
func dispatchRun(name string, dryRun bool, local bool) {
	if local {
		cfg := loadConfig()
		setupLogging(cfg.Storage.LogFile)
		cmdRun(cfg, name, dryRun)
		return
	}

	exit, err := control.Run(control.DefaultSocketPath(), control.Request{
		Cmd:    "run",
		Name:   name,
		DryRun: dryRun,
	}, os.Stderr)
	if err != nil {
		if errors.Is(err, control.ErrDaemonUnreachable) {
			fmt.Fprintf(os.Stderr,
				"goback: daemon control socket unreachable at %s\n"+
					"  start the daemon: launchctl load ~/Library/LaunchAgents/com.goback.daemon.plist\n"+
					"  or run in-process: goback run --local %s\n",
				control.DefaultSocketPath(), name)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "goback: %v\n", err)
		os.Exit(2)
	}
	os.Exit(exit)
}

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: goback <command> [args]

Commands:
  init              Create default config file
  auth              Resolve and cache op:// secrets in system keychain
  auth --clear      Remove cached secrets from system keychain
  clear [name]      Remove cached secrets from keychain (all if no name given)
  completion <shell> Output shell completion script (bash, zsh)
  daemon            Run the backup scheduler (foreground)
  run [name]        Manually trigger one or all backups (delegates to daemon)
  now               Run all backups immediately (delegates to daemon)
  dry-run [name]    Simulate backups (connect but don't transfer)

Flags for run/now/dry-run:
  --local           Run in-process instead of delegating to the daemon.
                    Use when the daemon isn't running or for debugging.
                    From cron/launchd, omit this flag so the daemon (which
                    has keychain access in the user GUI session) handles
                    the run.
  list              Show configured backup jobs
  status            Show recent backup history
  last <name>       Print timestamp of last successful backup (RFC3339; use --epoch for Unix seconds)

Service management (macOS):
  launchctl load ~/Library/LaunchAgents/com.goback.daemon.plist
  launchctl unload ~/Library/LaunchAgents/com.goback.daemon.plist
`)
}

func loadConfig() *config.Config {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	return cfg
}

func setupLogging(logFile string) *os.File {
	if logFile == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		log.Printf("warning: could not create log directory: %v", err)
		return nil
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("warning: could not open log file: %v", err)
		return nil
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	return f
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

func cmdClear(name string) {
	cfg, err := config.LoadRaw(config.DefaultPath())
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}

	type secret struct {
		key   string
		label string
	}

	var secrets []secret
	for _, b := range cfg.Backups {
		if name != "" && b.Name != name {
			continue
		}
		if strings.HasPrefix(b.HAToken, "op://") {
			secrets = append(secrets, secret{
				key:   "ha_token_" + b.Name,
				label: b.Name + " (ha_token)",
			})
		}
		if strings.HasPrefix(b.SSHKey, "op://") {
			secrets = append(secrets, secret{
				key:   "ssh_key_" + b.Name,
				label: b.Name + " (ssh_key)",
			})
		}
	}

	if name != "" && len(secrets) == 0 {
		log.Fatalf("no backup named %q with op:// secrets found in config", name)
	}
	if len(secrets) == 0 {
		fmt.Println("no op:// secrets found in config")
		return
	}

	for _, s := range secrets {
		if err := credentials.Delete(s.key); err != nil {
			log.Printf("error clearing %s: %v", s.label, err)
		} else {
			fmt.Printf("cleared %s from keychain\n", s.label)
		}
	}
}

func cmdDaemon(cfg *config.Config, tee *control.LogTee) {
	if err := daemon.RunWithPath(cfg, config.DefaultPath(), tee); err != nil {
		log.Fatalf("daemon error: %v", err)
	}
}

func cmdRun(cfg *config.Config, name string, dryRun bool) {
	store := daemon.NewStorage(cfg)
	// Interactive runs go single-attempt; the daemon path is what wants retry.
	opts := backup.Options{DryRun: dryRun, NoRetry: true}

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
				// backup.Execute already recorded the failure attempt.
				log.Printf("backup %s failed: %v", t.Name, err)
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
	store := daemon.NewStorage(cfg)

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

func cmdLast(cfg *config.Config, name string, epoch bool) {
	if name == "" {
		fmt.Fprintln(os.Stderr, "usage: goback last <name> [--epoch|-e]")
		os.Exit(1)
	}

	store := daemon.NewStorage(cfg)
	sl, err := store.LoadLog()
	if err != nil {
		log.Fatalf("loading status: %v", err)
	}

	for i := len(sl.Records) - 1; i >= 0; i-- {
		r := sl.Records[i]
		if r.Name == name && r.Error == "" {
			if epoch {
				fmt.Println(r.Timestamp.Unix())
			} else {
				fmt.Println(r.Timestamp.Format(time.RFC3339))
			}
			return
		}
	}

	fmt.Fprintf(os.Stderr, "no successful backup found for %q\n", name)
	os.Exit(1)
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
