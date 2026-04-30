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
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/msjurset/goback/internal/config"
	"github.com/msjurset/goback/internal/storage"
)

// HA timeouts. The parent ctx is the outer cap (set by Execute callers); these
// guard against silent stalls on flaky connections so we fail fast and let
// the retry layer (Stage 3) backoff cleanly.
const (
	haDialTimeout            = 10 * time.Second
	haTLSHandshakeTimeout    = 15 * time.Second
	haResponseHeaderTimeout  = 60 * time.Second
	haIdleConnTimeout        = 90 * time.Second
	haWSReadTimeout          = 30 * time.Second
	haCreateTimeout          = 60 * time.Second
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
	if err := p.createBackup(ctx, baseURL, cfg.HAToken); err != nil {
		return Result{}, fmt.Errorf("creating backup: %w", err)
	}

	log.Printf("[%s] waiting for new backup to appear", cfg.Name)
	backupID, err := p.waitForBackup(ctx, baseURL, cfg.HAToken, existingIDs)
	if err != nil {
		return Result{}, fmt.Errorf("waiting for backup: %w", err)
	}

	dest := store.BackupPath(cfg.Folder, cfg.Name, ".tar", cfg.Filename)
	log.Printf("[%s] downloading backup %s -> %s", cfg.Name, backupID, dest)
	size, err := p.downloadBackup(ctx, baseURL, cfg.HAToken, backupID, dest)
	if err != nil {
		return Result{}, fmt.Errorf("downloading backup: %w", err)
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

func (p *HAProvider) createBackup(ctx context.Context, baseURL, token string) error {
	// Bound the trigger request — HA replies as soon as the backup is queued.
	callCtx, cancel := context.WithTimeout(ctx, haCreateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, baseURL+"/api/services/hassio/backup_full", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := haClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create backup: HTTP %d: %s", resp.StatusCode, body)
	}

	return nil
}

func (p *HAProvider) waitForBackup(ctx context.Context, baseURL, token string, existingIDs map[string]bool) (string, error) {
	timeout := time.After(10 * time.Minute)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

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
