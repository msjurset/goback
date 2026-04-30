package backup

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/msjurset/goback/internal/config"
)

// notifyTimeout bounds the failure-notification command so a hung script
// (e.g. a webhook against a slow service) can't stall the daemon goroutine.
const notifyTimeout = 30 * time.Second

// notifyFailure runs cfg.NotifyCommand via `sh -c` after a retry-exhausted
// backup failure. The command receives details via env vars:
//
//	GOBACK_BACKUP_NAME, GOBACK_BACKUP_TYPE, GOBACK_ERROR, GOBACK_ATTEMPTS
//
// Errors from the notify command are logged but never propagated — failing
// to notify must not turn into a second failure mode that masks the original.
func notifyFailure(cfg config.BackupConfig, backupErr error, attempts int) {
	if cfg.NotifyCommand == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.NotifyCommand)
	cmd.Env = append(os.Environ(),
		"GOBACK_BACKUP_NAME="+cfg.Name,
		"GOBACK_BACKUP_TYPE="+cfg.Type,
		"GOBACK_ERROR="+backupErr.Error(),
		"GOBACK_ATTEMPTS="+strconv.Itoa(attempts),
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		log.Printf("[%s] notify_command failed: %v", cfg.Name, err)
	}
}
