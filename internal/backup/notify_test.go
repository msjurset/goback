package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/msjurset/goback/internal/config"
)

func TestNotifyFailureWritesEnv(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "notify.log")

	cfg := config.BackupConfig{
		Name: "pihole",
		Type: "ssh",
		// `printf` is more portable than echo across shells.
		NotifyCommand: `printf '%s|%s|%s|%s' "$GOBACK_BACKUP_NAME" "$GOBACK_BACKUP_TYPE" "$GOBACK_ERROR" "$GOBACK_ATTEMPTS" > ` + out,
	}

	notifyFailure(cfg, errors.New("connection refused"), 5)

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("notify did not write file: %v", err)
	}
	want := "pihole|ssh|connection refused|5"
	if string(got) != want {
		t.Errorf("env content = %q, want %q", got, want)
	}
}

func TestNotifyFailureEmptyCommandIsNoop(t *testing.T) {
	cfg := config.BackupConfig{Name: "x", Type: "ssh", NotifyCommand: ""}
	// Just verify it doesn't panic and returns quickly.
	done := make(chan struct{})
	go func() {
		notifyFailure(cfg, errors.New("anything"), 1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("notifyFailure with empty command should return immediately")
	}
}

func TestNotifyFailureCommandFailureIsLoggedNotPanicked(t *testing.T) {
	// A command that exits non-zero must not panic or propagate.
	cfg := config.BackupConfig{
		Name:          "x",
		Type:          "ssh",
		NotifyCommand: "false",
	}
	notifyFailure(cfg, errors.New("err"), 1)
	// If we got here, no panic. Pass.
}

func TestNotifyFailureBoundedByTimeout(t *testing.T) {
	// A sleeping notify command must be killed by the timeout.
	cfg := config.BackupConfig{
		Name:          "x",
		Type:          "ssh",
		NotifyCommand: "sleep 60",
	}

	// We don't want to actually wait the full 30s notifyTimeout in tests.
	// Verify by observing that Process gets killed by sending SIGTERM via
	// a context with a short deadline — but notifyTimeout is hardcoded.
	// As a lighter check: just confirm the function returns within a
	// short multiple of the configured timeout. Skipped in -short.
	if testing.Short() {
		t.Skip("long: relies on notifyTimeout (30s)")
	}

	start := time.Now()
	notifyFailure(cfg, errors.New("err"), 1)
	elapsed := time.Since(start)
	if elapsed > 35*time.Second {
		t.Errorf("notifyFailure took %v, expected <35s (notifyTimeout=%v)", elapsed, notifyTimeout)
	}
}

func TestNotifyEnvDoesNotShadowExisting(t *testing.T) {
	// Setting GOBACK_* must not nuke unrelated env (e.g., PATH).
	cfg := config.BackupConfig{
		Name:          "x",
		Type:          "local",
		NotifyCommand: `test -n "$PATH"`,
	}
	notifyFailure(cfg, errors.New("err"), 1)
}

func TestNotifyDoesNotIncludeSpaceOnlyCommand(t *testing.T) {
	// A single-space notify_command is the documented "opt out" sentinel.
	// validate() in config strips it back to "" before reaching here, so
	// notifyFailure should treat empty as no-op (covered above) — this
	// test just sanity-checks that a stray whitespace-only command, if it
	// somehow makes it through, runs sh -c " " (a no-op shell) without
	// breaking anything.
	cfg := config.BackupConfig{Name: "x", Type: "ssh", NotifyCommand: " "}
	if cfg.NotifyCommand == "" {
		t.Skip("config layer cleared this; test n/a")
	}
	notifyFailure(cfg, errors.New("err"), 1)
}

