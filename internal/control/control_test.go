package control

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/runlock"
)

func TestLogTeeFanOut(t *testing.T) {
	tee := NewLogTee()
	var a, b bytes.Buffer
	deregA := tee.Add(&a)
	deregB := tee.Add(&b)

	if _, err := tee.Write([]byte("hello\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if a.String() != "hello\n" || b.String() != "hello\n" {
		t.Fatalf("both subscribers should see write; got a=%q b=%q", a.String(), b.String())
	}

	deregA()
	a.Reset()
	b.Reset()
	if _, err := tee.Write([]byte("again\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if a.String() != "" {
		t.Fatalf("deregistered subscriber should not receive writes; got %q", a.String())
	}
	if b.String() != "again\n" {
		t.Fatalf("remaining subscriber should still receive; got %q", b.String())
	}
	deregB()
}

func TestLogTeeDoubleDeregisterIsSafe(t *testing.T) {
	tee := NewLogTee()
	var buf bytes.Buffer
	dereg := tee.Add(&buf)
	dereg()
	dereg() // must not panic
}

func TestLogTeeConcurrentWriters(t *testing.T) {
	tee := NewLogTee()
	var buf safeBuffer
	dereg := tee.Add(&buf)
	defer dereg()

	const n = 200
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			fmt.Fprintf(tee, "line-%d\n", i)
		}(i)
	}
	wg.Wait()

	got := buf.String()
	for i := range n {
		needle := fmt.Sprintf("line-%d\n", i)
		if !strings.Contains(got, needle) {
			t.Fatalf("missing %q in tee output", needle)
		}
	}
}

func TestServerPing(t *testing.T) {
	srv, sockPath, cancel := startTestServer(t, &config.Config{})
	defer cancel()
	_ = srv

	exit, err := Run(sockPath, Request{Cmd: "ping"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("Run ping: %v", err)
	}
	if exit != 0 {
		t.Fatalf("ping exit = %d, want 0", exit)
	}
}

func TestServerRunUnknownName(t *testing.T) {
	cfg := &config.Config{
		Backups: []config.BackupConfig{{Name: "exists", Type: "local"}},
	}
	srv, sockPath, cancel := startTestServer(t, cfg)
	defer cancel()
	_ = srv

	var stderr bytes.Buffer
	exit, err := Run(sockPath, Request{Cmd: "run", Name: "nope"}, &stderr)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if exit == 0 {
		t.Fatalf("expected non-zero exit for unknown backup; got 0")
	}
	if !strings.Contains(stderr.String(), `"nope"`) {
		t.Fatalf("expected stderr to mention the missing name; got %q", stderr.String())
	}
}

func TestStartRejectsLive(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "control.sock")

	tee := NewLogTee()
	first := NewServer(sockPath, &config.Config{}, nil, runlock.New(), tee)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := first.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	second := NewServer(sockPath, &config.Config{}, nil, runlock.New(), NewLogTee())
	err := second.Start(context.Background())
	if err == nil {
		t.Fatal("expected second Start to fail with a live daemon already bound")
	}
	if !errorsIs(err, ErrDaemonAlreadyRunning) {
		t.Fatalf("error = %v, want wrapping ErrDaemonAlreadyRunning", err)
	}

	exit, err := Run(sockPath, Request{Cmd: "ping"}, &bytes.Buffer{})
	if err != nil || exit != 0 {
		t.Fatalf("first daemon should still be live after rejected duplicate; ping exit=%d err=%v", exit, err)
	}
}

func TestStartReplacesStale(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "control.sock")
	if err := os.WriteFile(sockPath, nil, 0o600); err != nil {
		t.Fatalf("seeding stale socket: %v", err)
	}

	srv := NewServer(sockPath, &config.Config{}, nil, runlock.New(), NewLogTee())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start over stale socket: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	exit, err := Run(sockPath, Request{Cmd: "ping"}, &bytes.Buffer{})
	if err != nil || exit != 0 {
		t.Fatalf("ping after stale-socket replacement: exit=%d err=%v", exit, err)
	}
}

func TestClientDaemonUnreachable(t *testing.T) {
	// Path with no listener — dial must fail with ErrDaemonUnreachable.
	bogus := filepath.Join(t.TempDir(), "no-such.sock")
	_, err := Run(bogus, Request{Cmd: "ping"}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected error dialing missing socket")
	}
	if !errorsIs(err, ErrDaemonUnreachable) {
		t.Fatalf("error = %v, want wrapping ErrDaemonUnreachable", err)
	}
}

// startTestServer binds the server on a temp socket and returns its address.
// The cancel function tears down the listener.
func startTestServer(t *testing.T, cfg *config.Config) (*Server, string, func()) {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "control.sock")

	tee := NewLogTee()
	srv := NewServer(sockPath, cfg, nil, runlock.New(), tee)

	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx); err != nil {
		cancel()
		t.Fatalf("Start: %v", err)
	}

	// Give the listener a beat to be ready before tests dial.
	time.Sleep(20 * time.Millisecond)
	return srv, sockPath, cancel
}

// errorsIs is a tiny wrapper so the test file doesn't need its own errors
// import just for this single use; equivalent to errors.Is.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		type wrap interface{ Unwrap() error }
		if w, ok := err.(wrap); ok {
			err = w.Unwrap()
			continue
		}
		return false
	}
	return false
}

// safeBuffer is a bytes.Buffer guarded by a mutex for concurrent writers.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
