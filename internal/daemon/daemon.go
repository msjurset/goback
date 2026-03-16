package daemon

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/msjurset/goback/internal/catchup"
	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/scheduler"
	"github.com/msjurset/goback/internal/storage"
)

func Run(cfg *config.Config) error {
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

	checker := catchup.NewChecker(cfg.Backups, store)
	go checker.Run(ctx)

	<-ctx.Done()
	log.Println("shutting down...")

	shutdownCtx := sched.Stop()
	<-shutdownCtx.Done()

	log.Println("daemon stopped")
	return nil
}
