package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

// gobackBackupName is the value goback sets in the HA backup `name` field
// when triggering creation. pruneHABackups uses this prefix to filter which
// remote backups it's allowed to remove — anything created by HA's own
// nightly automatic backup config (or by the user from the UI) keeps a
// different name and is never touched. Change this and you orphan every
// previously-created goback backup on the HA side.
const gobackBackupName = "goback"

// HA timeouts. The parent ctx is the outer cap (set by Execute callers); these
// guard against silent stalls on flaky connections so we fail fast and let
// the retry layer (Stage 3) backoff cleanly.
const (
	haDialTimeout            = 10 * time.Second
	haTLSHandshakeTimeout    = 15 * time.Second
	haResponseHeaderTimeout  = 60 * time.Second
	haIdleConnTimeout        = 90 * time.Second
	haWSReadTimeout          = 30 * time.Second
	// haCreateTimeout caps the synchronous backup-trigger POST. HA core's
	// service call to hassio.backup_full is `blocking=True` and only returns
	// after supervisor completes the backup — full HA backups commonly take
	// 5-15 min. We can't fire-and-forget: cancelling the request propagates
	// into services.async_call and supervisor never starts the work.
	haCreateTimeout          = 15 * time.Minute
	// haHeartbeatInterval is how often we log "still working" lines so users
	// watching the runbook step see liveness during the multi-minute trigger
	// and poll waits.
	haHeartbeatInterval      = 30 * time.Second
)

var haClient = &http.Client{
	Transport: &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   haDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   haTLSHandshakeTimeout,
		ResponseHeaderTimeout: haResponseHeaderTimeout,
		IdleConnTimeout:       haIdleConnTimeout,
	},
}

// wsRead sets a read deadline before ReadJSON so a half-dead WebSocket
// surfaces as a deadline error instead of blocking forever.
func wsRead(conn *websocket.Conn, msg interface{}) error {
	if err := conn.SetReadDeadline(time.Now().Add(haWSReadTimeout)); err != nil {
		return err
	}
	return conn.ReadJSON(msg)
}

type HAProvider struct{}

type haBackup struct {
	BackupID string `json:"backup_id"`
	Name     string `json:"name"`
	Date     string `json:"date"`
}

type wsMessage struct {
	ID      int             `json:"id,omitempty"`
	Type    string          `json:"type"`
	Success bool            `json:"success,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

type wsBackupInfo struct {
	Backups []haBackup `json:"backups"`
}

type wsSignPath struct {
	Path string `json:"path"`
}

func (p *HAProvider) Run(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) (Result, error) {
	baseURL := strings.TrimRight(cfg.HAUrl, "/")

	if opts.DryRun {
		return Result{}, p.dryRun(ctx, baseURL, cfg)
	}

	existingIDs, err := p.listBackupIDs(ctx, baseURL, cfg.HAToken)
	if err != nil {
		return Result{}, fmt.Errorf("listing existing backups: %w", err)
	}

	log.Printf("[%s] triggering backup creation", cfg.Name)
	if err := p.createBackup(ctx, cfg.Name, baseURL, cfg.HAToken); err != nil {
		return Result{}, fmt.Errorf("creating backup: %w", err)
	}

	log.Printf("[%s] waiting for new backup to appear", cfg.Name)
	backupID, err := p.waitForBackup(ctx, cfg.Name, baseURL, cfg.HAToken, existingIDs)
	if err != nil {
		return Result{}, fmt.Errorf("waiting for backup: %w", err)
	}

	dest := store.BackupPath(cfg.Folder, cfg.Name, ".tar", cfg.Filename)
	log.Printf("[%s] downloading backup %s -> %s", cfg.Name, backupID, dest)
	size, err := p.downloadBackup(ctx, baseURL, cfg.HAToken, backupID, dest)
	if err != nil {
		return Result{}, fmt.Errorf("downloading backup: %w", err)
	}

	// Prune old goback-tagged backups from the HA side. Mirrors the local
	// retention in storage.EnforceRetention — without this, HA accumulates
	// our backups forever and eventually fills its disk, which makes
	// supervisor silently reject future create requests with "not enough
	// free space" (HA returns 200 for the service call regardless).
	if err := p.pruneHABackups(ctx, cfg.Name, baseURL, cfg.HAToken, cfg.Retention); err != nil {
		log.Printf("[%s] warning: HA-side retention cleanup failed: %v", cfg.Name, err)
	}

	return Result{Filename: filepath.Base(dest), Size: size}, nil
}

func (p *HAProvider) listBackupIDs(ctx context.Context, baseURL, token string) (map[string]bool, error) {
	ws, err := p.connect(ctx, baseURL, token)
	if err != nil {
		return nil, err
	}
	defer ws.Close()

	backups, err := p.listBackups(ws)
	if err != nil {
		return nil, err
	}

	ids := make(map[string]bool)
	for _, b := range backups {
		ids[b.BackupID] = true
	}
	return ids, nil
}

func (p *HAProvider) dryRun(ctx context.Context, baseURL string, cfg config.BackupConfig) error {
	ws, err := p.connect(ctx, baseURL, cfg.HAToken)
	if err != nil {
		return fmt.Errorf("api connection: %w", err)
	}
	defer ws.Close()

	log.Printf("[%s] api connection to %s: ok", cfg.Name, cfg.HAUrl)

	backups, err := p.listBackups(ws)
	if err != nil {
		return fmt.Errorf("listing backups: %w", err)
	}

	log.Printf("[%s] existing backups on server: %d", cfg.Name, len(backups))
	for _, b := range backups {
		log.Printf("[%s]   %s (%s)", cfg.Name, b.BackupID, b.Date)
	}

	return nil
}

var msgID atomic.Int64

func nextID() int {
	return int(msgID.Add(1))
}

func (p *HAProvider) connect(ctx context.Context, baseURL, token string) (*websocket.Conn, error) {
	wsURL := strings.Replace(baseURL, "http://", "ws://", 1)
	wsURL = strings.Replace(wsURL, "https://", "wss://", 1)
	wsURL += "/api/websocket"

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dialing websocket: %w", err)
	}

	// Read auth_required
	var msg wsMessage
	if err := wsRead(conn, &msg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("reading auth_required: %w", err)
	}

	// Send auth
	if err := conn.WriteJSON(map[string]string{
		"type":         "auth",
		"access_token": token,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("sending auth: %w", err)
	}

	// Read auth result
	if err := wsRead(conn, &msg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("reading auth result: %w", err)
	}
	if msg.Type != "auth_ok" {
		conn.Close()
		return nil, fmt.Errorf("auth failed: %s", msg.Type)
	}

	return conn, nil
}

func (p *HAProvider) sendAndReceive(ws *websocket.Conn, msgType string, extra map[string]string) (json.RawMessage, error) {
	id := nextID()
	payload := map[string]interface{}{
		"id":   id,
		"type": msgType,
	}
	for k, v := range extra {
		payload[k] = v
	}

	if err := ws.WriteJSON(payload); err != nil {
		return nil, err
	}

	for {
		var msg wsMessage
		if err := wsRead(ws, &msg); err != nil {
			return nil, err
		}
		if msg.ID == id {
			if !msg.Success {
				return nil, fmt.Errorf("command %s failed", msgType)
			}
			return msg.Result, nil
		}
		// Skip events from subscriptions
	}
}

func (p *HAProvider) listBackups(ws *websocket.Conn) ([]haBackup, error) {
	result, err := p.sendAndReceive(ws, "backup/info", nil)
	if err != nil {
		return nil, err
	}

	var info wsBackupInfo
	if err := json.Unmarshal(result, &info); err != nil {
		return nil, err
	}
	return info.Backups, nil
}

func (p *HAProvider) createBackup(ctx context.Context, name, baseURL, token string) error {
	// Trigger via HA core backup integration's WebSocket `backup/generate`
	// command instead of POST /api/services/hassio/backup_full. The
	// service-call path silently swallows supervisor errors (returns 200
	// even when supervisor refuses the request — we lost a full 15-min
	// waitForBackup cycle to a "not enough free space" rejection that
	// never surfaced). backup/generate validates upfront, queues the job
	// async, and returns a structured WS result — success means the job
	// is in supervisor's queue, failure carries the real error message.
	ws, err := p.connect(ctx, baseURL, token)
	if err != nil {
		return fmt.Errorf("websocket connect for create: %w", err)
	}
	defer ws.Close()

	stopHeartbeat := startHeartbeat(ctx, name, "still queuing backup")
	defer stopHeartbeat()

	// agent_ids targets the supervisor-managed storage so the resulting
	// .tar lands in /backup on the HAOS host — same location goback's
	// download path expects. include_all_addons + include_homeassistant +
	// include_database mirrors what `hassio.backup_full` does (a true full
	// supervisor backup); folders default to empty because supervisor
	// auto-adds the standard set when targeting hassio.local.
	id := nextID()
	if err := ws.WriteJSON(map[string]interface{}{
		"id":                 id,
		"type":               "backup/generate",
		"agent_ids":          []string{"hassio.local"},
		"include_all_addons": true,
		"include_database":   true,
		"include_homeassistant": true,
		"name":               gobackBackupName,
	}); err != nil {
		return fmt.Errorf("sending backup/generate: %w", err)
	}

	// Read until we see our reply (skipping any subscription events that
	// happen to arrive on this connection). wsRead's 30s deadline is fine
	// — HA returns the queue-acknowledgement quickly; the actual backup
	// runs async in supervisor and is detected later by waitForBackup.
	for {
		var msg wsMessage
		if err := wsRead(ws, &msg); err != nil {
			return fmt.Errorf("reading backup/generate result: %w", err)
		}
		if msg.ID != id {
			continue
		}
		if !msg.Success {
			// HA's WS protocol packs the failure detail in msg.Result as
			// a structured error object. We carry it through verbatim so
			// callers (and the runbook log) see the real reason — e.g.
			// "not enough free space (1.7GB) left on the device".
			return fmt.Errorf("backup/generate rejected: %s", string(msg.Result))
		}
		return nil
	}
}

// pruneHABackups removes goback-tagged backups from the HA side, keeping
// the most recent `keep`. Filtering by name prefix protects HA's own
// nightly Automatic_backup_* entries and any user-triggered ones. Uses
// the HA core backup integration's `backup/delete` WS command, which
// delegates to supervisor for backups stored on hassio agents — same
// path the `ha backups remove <slug>` CLI command takes.
func (p *HAProvider) pruneHABackups(ctx context.Context, name, baseURL, token string, keep int) error {
	if keep <= 0 {
		return nil
	}

	ws, err := p.connect(ctx, baseURL, token)
	if err != nil {
		return fmt.Errorf("connect for prune: %w", err)
	}
	defer ws.Close()

	backups, err := p.listBackups(ws)
	if err != nil {
		return fmt.Errorf("listing for prune: %w", err)
	}

	var ours []haBackup
	for _, b := range backups {
		if b.Name == gobackBackupName {
			ours = append(ours, b)
		}
	}

	if len(ours) <= keep {
		log.Printf("[%s] HA-side retention: %d/%d goback backups (no prune needed)", name, len(ours), keep)
		return nil
	}

	// HA returns Date as RFC3339Nano-ish but with mixed timezones across
	// entries, so parse before comparing rather than sorting strings.
	sort.Slice(ours, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339Nano, ours[i].Date)
		tj, _ := time.Parse(time.RFC3339Nano, ours[j].Date)
		return ti.After(tj)
	})

	toDelete := ours[keep:]
	log.Printf("[%s] HA-side retention: pruning %d of %d goback backups (keeping %d most recent)", name, len(toDelete), len(ours), keep)
	for _, b := range toDelete {
		log.Printf("[%s] removing remote backup %s (%s)", name, b.BackupID, b.Date)
		if _, err := p.sendAndReceive(ws, "backup/delete", map[string]string{"backup_id": b.BackupID}); err != nil {
			log.Printf("[%s] warning: failed to delete %s: %v", name, b.BackupID, err)
		}
	}
	return nil
}

// startHeartbeat logs a "still <action> (Ns elapsed)" line every
// haHeartbeatInterval until the returned stop function is called. Wired into
// long-running synchronous calls (createBackup, waitForBackup) so users
// watching a runbook step see liveness rather than a 10-minute silence.
func startHeartbeat(ctx context.Context, name, action string) func() {
	stop := make(chan struct{})
	start := time.Now()
	go func() {
		ticker := time.NewTicker(haHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Printf("[%s] %s (%s elapsed)", name, action, time.Since(start).Round(time.Second))
			}
		}
	}()
	return func() { close(stop) }
}

func (p *HAProvider) waitForBackup(ctx context.Context, name, baseURL, token string, existingIDs map[string]bool) (string, error) {
	timeout := time.After(15 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	stopHeartbeat := startHeartbeat(ctx, name, "still waiting for backup to appear")
	defer stopHeartbeat()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout:
			return "", fmt.Errorf("timed out waiting for backup to complete")
		case <-ticker.C:
			ws, err := p.connect(ctx, baseURL, token)
			if err != nil {
				log.Printf("warning: reconnect failed: %v", err)
				continue
			}
			backups, err := p.listBackups(ws)
			ws.Close()
			if err != nil {
				log.Printf("warning: error polling backups: %v", err)
				continue
			}
			for _, b := range backups {
				if !existingIDs[b.BackupID] {
					return b.BackupID, nil
				}
			}
		}
	}
}

func (p *HAProvider) downloadBackup(ctx context.Context, baseURL, token, backupID, dest string) (int64, error) {
	ws, err := p.connect(ctx, baseURL, token)
	if err != nil {
		return 0, fmt.Errorf("websocket connect for download: %w", err)
	}
	defer ws.Close()

	// Get a signed download URL via WebSocket
	signPath := fmt.Sprintf("/api/backup/download/%s?agent_id=hassio.local", backupID)
	result, err := p.sendAndReceive(ws, "auth/sign_path", map[string]string{
		"path": signPath,
	})
	if err != nil {
		return 0, fmt.Errorf("signing download path: %w", err)
	}

	var signed wsSignPath
	if err := json.Unmarshal(result, &signed); err != nil {
		return 0, err
	}

	// Download using the signed URL (no auth header needed). The parent ctx
	// is the outer cap; haClient.Transport.ResponseHeaderTimeout guards
	// against the server going silent before sending headers.
	downloadURL := baseURL + signed.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := haClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("download backup: HTTP %d: %s", resp.StatusCode, body)
	}

	expected := int64(-1)
	if resp.ContentLength >= 0 {
		expected = resp.ContentLength
	}

	return AtomicDownload(dest, expected, func(w io.Writer) (int64, error) {
		return io.Copy(w, resp.Body)
	})
}
