package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

type Manager struct {
	baseDir string
}

type BackupRecord struct {
	Name      string    `json:"name"`
	Filename  string    `json:"filename"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size"`
	Error     string    `json:"error,omitempty"`
}

type StatusLog struct {
	Records []BackupRecord `json:"records"`
}

func New(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

func (m *Manager) BackupDir(folder string) string {
	return filepath.Join(m.baseDir, folder)
}

func (m *Manager) EnsureDir(folder string) error {
	return os.MkdirAll(m.BackupDir(folder), 0o755)
}

var templateRe = regexp.MustCompile(`\{([^}]+)\}`)

func (m *Manager) BackupPath(folder, name, ext, filenameTmpl string) string {
	if filenameTmpl == "" {
		ts := time.Now().Format("20060102-150405")
		return filepath.Join(m.BackupDir(folder), fmt.Sprintf("%s-%s%s", name, ts, ext))
	}
	now := time.Now()
	expanded := templateRe.ReplaceAllStringFunc(filenameTmpl, func(match string) string {
		layout := match[1 : len(match)-1]
		return now.Format(layout)
	})
	return filepath.Join(m.BackupDir(folder), expanded)
}

func (m *Manager) logPath() string {
	return filepath.Join(m.baseDir, "status.json")
}

func (m *Manager) LoadLog() (*StatusLog, error) {
	data, err := os.ReadFile(m.logPath())
	if os.IsNotExist(err) {
		return &StatusLog{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading status log: %w", err)
	}

	var log StatusLog
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, fmt.Errorf("parsing status log: %w", err)
	}
	return &log, nil
}

func (m *Manager) RecordBackup(rec BackupRecord) error {
	if err := os.MkdirAll(m.baseDir, 0o755); err != nil {
		return err
	}

	log, err := m.LoadLog()
	if err != nil {
		log = &StatusLog{}
	}

	log.Records = append(log.Records, rec)

	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.logPath(), data, 0o644)
}

func (m *Manager) EnforceRetention(folder string, keep int) error {
	dir := m.BackupDir(folder)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	type fileEntry struct {
		name    string
		modTime time.Time
	}

	var files []fileEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{name: e.Name(), modTime: info.ModTime()})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})

	for i := keep; i < len(files); i++ {
		path := filepath.Join(dir, files[i].name)
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("removing old backup %s: %w", path, err)
		}
	}

	return nil
}

func (m *Manager) ListBackups(folder string) ([]os.DirEntry, error) {
	dir := m.BackupDir(folder)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return entries, err
}
