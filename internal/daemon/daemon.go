package daemon

import (
	"context"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/msjurset/goback/internal/catchup"
	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/scheduler"
	"github.com/msjurset/goback/internal/storage"
)

func Run(cfg *config.Config) error {
	return RunWithPath(cfg, config.DefaultPath())
}

func RunWithPath(cfg *config.Config, configPath string) error {
	store := storage.New(cfg.Storage.BaseDir)
	sched := scheduler.New(store)

	for _, b := range cfg.Backups {
		if err := sched.Add(b); err != nil {
			return err
		}
	}

	sched.Start()
	log.Printf("goback daemon started with %d backup jobs", len(cfg.Backups))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Watch config file for changes
	go watchConfig(ctx, configPath, sched, store)

	// Watch binary for changes (let launchd restart us)
	ctx, cancelBinary := watchBinary(ctx)
	defer cancelBinary()

	checker := catchup.NewChecker(cfg.Backups, store)
	go checker.Run(ctx)

	<-ctx.Done()
	log.Println("shutting down...")

	shutdownCtx := sched.Stop()
	<-shutdownCtx.Done()

	log.Println("daemon stopped")
	return nil
}

// watchConfig monitors the config file for changes and rebuilds the scheduler.
func watchConfig(ctx context.Context, configPath string, sched *scheduler.Scheduler, store *storage.Manager) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("config watcher: failed to create: %v", err)
		return
	}
	defer watcher.Close()

	// Watch the parent directory (fsnotify works better on dirs)
	dir := filepath.Dir(configPath)
	if err := watcher.Add(dir); err != nil {
		log.Printf("config watcher: failed to watch %s: %v", dir, err)
		return
	}

	var debounce *time.Timer
	base := filepath.Base(configPath)

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) != base {
				continue
			}
			if !event.Has(fsnotify.Write) && !event.Has(fsnotify.Create) {
				continue
			}
			// Debounce: editors often write multiple times
			if debounce != nil {
				debounce.Stop()
			}
			debounce = time.AfterFunc(500*time.Millisecond, func() {
				reloadConfig(configPath, sched, store)
			})
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("config watcher error: %v", err)
		}
	}
}

// reloadConfig loads the config and rebuilds the scheduler.
func reloadConfig(configPath string, sched *scheduler.Scheduler, store *storage.Manager) {
	log.Printf("config change detected, reloading...")

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("config reload failed (keeping current config): %v", err)
		return
	}

	// Rebuild scheduler with new config
	sched.ReplaceAll(cfg.Backups)
	log.Printf("config reloaded: %d backup jobs", len(cfg.Backups))
}

// watchBinary polls the binary for changes and cancels the context if modified.
// Launchd's KeepAlive restarts the process with the new binary.
func watchBinary(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)

	exe, err := os.Executable()
	if err != nil {
		log.Printf("binary watcher: cannot determine executable path: %v", err)
		return ctx, cancel
	}

	info, err := os.Stat(exe)
	if err != nil {
		log.Printf("binary watcher: cannot stat %s: %v", exe, err)
		return ctx, cancel
	}
	startMod := info.ModTime()

	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := os.Stat(exe)
				if err != nil {
					continue
				}
				if !info.ModTime().Equal(startMod) {
					log.Printf("binary changed (%s), restarting...", exe)
					cancel()
					return
				}
			}
		}
	}()

	return ctx, cancel
}
