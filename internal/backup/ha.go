package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/msjurseth/markback/internal/config"
	"github.com/msjurseth/markback/internal/storage"
)

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

func (p *HAProvider) Run(ctx context.Context, cfg config.BackupConfig, store *storage.Manager, opts Options) error {
	baseURL := strings.TrimRight(cfg.HAUrl, "/")

	if opts.DryRun {
		return p.dryRun(ctx, baseURL, cfg)
	}

	// List existing backups (fresh connection)
	existingIDs, err := p.listBackupIDs(ctx, baseURL, cfg.HAToken)
	if err != nil {
		return fmt.Errorf("listing existing backups: %w", err)
	}

	// Trigger backup creation via REST
	log.Printf("[%s] triggering backup creation", cfg.Name)
	if err := p.createBackup(ctx, baseURL, cfg.HAToken); err != nil {
		return fmt.Errorf("creating backup: %w", err)
	}

	// Poll for new backup (reconnects on each poll)
	log.Printf("[%s] waiting for new backup to appear", cfg.Name)
	backupID, err := p.waitForBackup(ctx, baseURL, cfg.HAToken, existingIDs)
	if err != nil {
		return fmt.Errorf("waiting for backup: %w", err)
	}

	// Download via signed URL (fresh connection)
	dest := store.BackupPath(cfg.Folder, cfg.Name, ".tar", cfg.Filename)
	log.Printf("[%s] downloading backup %s -> %s", cfg.Name, backupID, dest)
	size, err := p.downloadBackup(ctx, baseURL, cfg.HAToken, backupID, dest)
	if err != nil {
		return fmt.Errorf("downloading backup: %w", err)
	}

	return store.RecordBackup(storage.BackupRecord{
		Name:      cfg.Name,
		Filename:  filepath.Base(dest),
		Timestamp: time.Now(),
		Size:      size,
	})
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
	if err := conn.ReadJSON(&msg); err != nil {
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
	if err := conn.ReadJSON(&msg); err != nil {
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
		if err := ws.ReadJSON(&msg); err != nil {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/services/hassio/backup_full", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
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

	// Download using the signed URL (no auth header needed)
	downloadURL := baseURL + signed.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return 0, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("download backup: HTTP %d: %s", resp.StatusCode, body)
	}

	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	return io.Copy(f, resp.Body)
}
