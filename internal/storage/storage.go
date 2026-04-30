package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

type Manager struct {
	baseDir          string
	recordsPerBackup int
	mu               sync.Mutex // guards status.json read-modify-write
}

// DefaultRecordsPerBackup is the per-name cap applied when none is configured.
const DefaultRecordsPerBackup = 200

type BackupRecord struct {
	Name      string    `json:"name"`
	Filename  string    `json:"filename,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Size      int64     `json:"size,omitempty"`
	Error     string    `json:"error,omitempty"`
	Attempt   int       `json:"attempt,omitempty"`
}

type StatusLog struct {
	Records []BackupRecord `json:"records"`
}

func New(baseDir string) *Manager {
	return &Manager{baseDir: baseDir, recordsPerBackup: DefaultRecordsPerBackup}
}

// NewWithCap returns a Manager that keeps at most recordsPerBackup history
// records per backup name. A non-positive value disables the cap.
func NewWithCap(baseDir string, recordsPerBackup int) *Manager {
	if recordsPerBackup <= 0 {
		recordsPerBackup = 0 // 0 means "no cap"
	}
	return &Manager{baseDir: baseDir, recordsPerBackup: recordsPerBackup}
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

	m.mu.Lock()
	defer m.mu.Unlock()

	log, err := m.LoadLog()
	if err != nil {
		log = &StatusLog{}
	}

	log.Records = append(log.Records, rec)
	log.Records = trimRecords(log.Records, m.recordsPerBackup)

	data, err := json.MarshalIndent(log, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to .tmp in the same directory, then rename. A crash
	// before the rename leaves the prior status.json intact. Records may include
	// error strings echoing URLs/tokens, so use 0o600.
	tmp := m.logPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, m.logPath()); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
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

// trimRecords keeps the most recent perName records for each backup name and
// drops the rest. Original ordering of the kept records is preserved so the
// status log stays append-ordered. perName <= 0 disables trimming.
func trimRecords(records []BackupRecord, perName int) []BackupRecord {
	if perName <= 0 || len(records) == 0 {
		return records
	}

	// Index records by name, sorted by timestamp newest-first, kept indices.
	byName := make(map[string][]int, 8)
	for i, r := range records {
		byName[r.Name] = append(byName[r.Name], i)
	}

	keep := make(map[int]struct{}, len(records))
	for _, idxs := range byName {
		if len(idxs) <= perName {
			for _, i := range idxs {
				keep[i] = struct{}{}
			}
			continue
		}
		sort.Slice(idxs, func(a, b int) bool {
			return records[idxs[a]].Timestamp.After(records[idxs[b]].Timestamp)
		})
		for _, i := range idxs[:perName] {
			keep[i] = struct{}{}
		}
	}

	if len(keep) == len(records) {
		return records
	}
	out := make([]BackupRecord, 0, len(keep))
	for i, r := range records {
		if _, ok := keep[i]; ok {
			out = append(out, r)
		}
	}
	return out
}

func (m *Manager) ListBackups(folder string) ([]os.DirEntry, error) {
	dir := m.BackupDir(folder)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return entries, err
}
