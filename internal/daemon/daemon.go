package daemon

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/msjurseth/markback/internal/config"
	"github.com/msjurseth/markback/internal/scheduler"
	"github.com/msjurseth/markback/internal/storage"
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
	log.Printf("markback daemon started with %d backup jobs", len(cfg.Backups))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	<-ctx.Done()
	log.Println("shutting down...")

	shutdownCtx := sched.Stop()
	<-shutdownCtx.Done()

	log.Println("daemon stopped")
	return nil
}
