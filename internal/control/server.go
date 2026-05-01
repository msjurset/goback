package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/msjurset/goback/internal/backup"
	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/runlock"
	"github.com/msjurset/goback/internal/storage"
)

// LogTee is an io.Writer that fans Write calls out to a dynamically managed
// set of subscriber writers. The daemon installs LogTee as one of the
// destinations of the global log.Logger; a control connection that's running
// a backup subscribes for the duration of the run so log.Printf output
// streams back to its client.
type LogTee struct {
	mu      sync.Mutex
	next    int
	writers map[int]io.Writer
}

// NewLogTee returns a tee with no subscribers. Writes are no-ops until at
// least one writer is added.
func NewLogTee() *LogTee {
	return &LogTee{writers: make(map[int]io.Writer)}
}

// Write forwards p to every currently-registered subscriber. Subscriber
// errors are swallowed so a slow or dead client never breaks the daemon's
// own logging path.
func (t *LogTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	subs := make([]io.Writer, 0, len(t.writers))
	for _, w := range t.writers {
		subs = append(subs, w)
	}
	t.mu.Unlock()
	for _, w := range subs {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

// Add registers w as a subscriber and returns a deregister function. Calling
// the returned function more than once is safe; only the first call has
// effect.
func (t *LogTee) Add(w io.Writer) func() {
	t.mu.Lock()
	id := t.next
	t.next++
	t.writers[id] = w
	t.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.writers, id)
			t.mu.Unlock()
		})
	}
}

// Server accepts client connections on a Unix socket and runs backups using
// the daemon's already-resolved config. Output streams back per-connection.
type Server struct {
	socketPath string
	store      *storage.Manager
	coord      *runlock.Coordinator
	tee        *LogTee

	mu       sync.RWMutex
	cfg      *config.Config // swapped on reload via SetConfig
	listener net.Listener
}

// NewServer constructs a server with no listener yet. Call Start to bind.
func NewServer(socketPath string, cfg *config.Config, store *storage.Manager, coord *runlock.Coordinator, tee *LogTee) *Server {
	return &Server{
		socketPath: socketPath,
		cfg:        cfg,
		store:      store,
		coord:      coord,
		tee:        tee,
	}
}

// SetConfig swaps the active config snapshot. Called by the daemon after a
// successful config reload so newly added backups become invokable without
// daemon restart.
func (s *Server) SetConfig(cfg *config.Config) {
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
}

func (s *Server) currentConfig() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// ErrDaemonAlreadyRunning is returned by Start when another live daemon is
// already bound to the control socket. Callers should treat this as fatal
// rather than continuing — two schedulers fighting over the same backups
// duplicates work and races on storage.
var ErrDaemonAlreadyRunning = errors.New("another goback daemon is already running")

// Start binds the socket, applies 0600 permissions, and begins accepting
// connections in a background goroutine. When ctx is cancelled the listener
// is closed and the socket file removed.
//
// If the socket file already exists, Start probes it: a live daemon means
// hard fail (ErrDaemonAlreadyRunning); a stale file from a previously
// crashed daemon is unlinked and we proceed.
func (s *Server) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.socketPath), 0o755); err != nil {
		return fmt.Errorf("creating socket dir: %w", err)
	}
	if _, err := os.Stat(s.socketPath); err == nil {
		if probeLiveDaemon(s.socketPath) {
			return fmt.Errorf("%w (socket %s)", ErrDaemonAlreadyRunning, s.socketPath)
		}
		// Stale socket from a crashed daemon — safe to unlink because
		// filesystem perms (0600) gate who can write here.
		_ = os.Remove(s.socketPath)
	}

	l, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", s.socketPath, err)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		l.Close()
		return fmt.Errorf("chmod socket: %w", err)
	}
	s.listener = l
	log.Printf("control: listening on %s", s.socketPath)

	go func() {
		<-ctx.Done()
		l.Close()
		_ = os.Remove(s.socketPath)
	}()

	go s.acceptLoop()
	return nil
}

// probeLiveDaemon tries a short ping over an existing socket file to
// distinguish a running daemon from a stale unix socket left by a crash.
// Returns true only on a clean ping/pong; any error path is treated as
// "stale" so a permissions or partial-write hiccup doesn't lock us out.
func probeLiveDaemon(path string) bool {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(conn).Encode(Request{Cmd: "ping"}); err != nil {
		return false
	}
	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return false
	}
	return resp.Type == "result" && resp.Exit == 0
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("control: accept error: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

// connSink turns conn writes into JSON-encoded Response messages, with mu
// guarding concurrent writers (log tee + result sender).
type connSink struct {
	conn net.Conn
	mu   *sync.Mutex
}

func (s *connSink) send(resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.conn.Write(data)
	return err
}

// logWriter satisfies io.Writer so the daemon's log.Printf calls can flow
// into a control connection while the connection holds a tee subscription.
type logWriter struct {
	sink *connSink
}

func (w *logWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line == "" {
		return len(p), nil
	}
	_ = w.sink.send(Response{Type: "log", Stream: "stderr", Line: line})
	return len(p), nil
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	// Cap how long we'll wait for the request line — clients should send it
	// immediately after connecting.
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	dec := json.NewDecoder(conn)
	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = conn.SetReadDeadline(time.Time{})
		mu := &sync.Mutex{}
		sink := &connSink{conn: conn, mu: mu}
		_ = sink.send(Response{Type: "result", Exit: 2, Error: "invalid request: " + err.Error()})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	mu := &sync.Mutex{}
	sink := &connSink{conn: conn, mu: mu}

	switch req.Cmd {
	case "ping":
		_ = sink.send(Response{Type: "result", Exit: 0})
	case "run":
		s.runBackup(req, sink)
	default:
		_ = sink.send(Response{Type: "result", Exit: 2, Error: "unknown cmd: " + req.Cmd})
	}
}

func (s *Server) runBackup(req Request, sink *connSink) {
	cfg := s.currentConfig()

	var targets []config.BackupConfig
	if req.Name == "" {
		targets = cfg.Backups
	} else {
		for _, b := range cfg.Backups {
			if b.Name == req.Name {
				targets = append(targets, b)
				break
			}
		}
		if len(targets) == 0 {
			_ = sink.send(Response{Type: "result", Exit: 1, Error: fmt.Sprintf("no backup named %q in config", req.Name)})
			return
		}
	}

	// Subscribe to log output for the duration of the run. After we
	// deregister, late log lines (e.g., from a goroutine the provider
	// spawned that hasn't yet exited) won't reach the client — but
	// backup.Execute runs synchronously, so this is fine in practice.
	deregister := s.tee.Add(&logWriter{sink: sink})
	defer deregister()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer cancel()

	var firstErr error
	for _, t := range targets {
		release, ok := s.coord.TryLock(t.Name)
		if !ok {
			log.Printf("control: %s already running, skipping", t.Name)
			continue
		}

		opts := backup.Options{DryRun: req.DryRun, NoRetry: true}
		err := backup.Execute(ctx, t, s.store, opts)
		release()
		if err != nil {
			log.Printf("control: backup %s failed: %v", t.Name, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	if firstErr != nil {
		_ = sink.send(Response{Type: "result", Exit: 1, Error: firstErr.Error()})
		return
	}
	_ = sink.send(Response{Type: "result", Exit: 0})
}
