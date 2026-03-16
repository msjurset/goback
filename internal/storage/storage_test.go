package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	m := New("/tmp/backups")
	if m.baseDir != "/tmp/backups" {
		t.Errorf("baseDir = %q, want %q", m.baseDir, "/tmp/backups")
	}
}

func TestBackupDir(t *testing.T) {
	m := New("/tmp/backups")
	got := m.BackupDir("pihole")
	want := "/tmp/backups/pihole"
	if got != want {
		t.Errorf("BackupDir() = %q, want %q", got, want)
	}
}

func TestEnsureDir(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	if err := m.EnsureDir("test-backup"); err != nil {
		t.Fatalf("EnsureDir() error: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "test-backup"))
	if err != nil {
		t.Fatalf("created directory not found: %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir() did not create a directory")
	}
}

func TestBackupPathDefault(t *testing.T) {
	m := New("/tmp/backups")
	path := m.BackupPath("pihole", "pihole", ".zip", "")

	if !strings.HasPrefix(path, "/tmp/backups/pihole/pihole-") {
		t.Errorf("BackupPath() = %q, want prefix %q", path, "/tmp/backups/pihole/pihole-")
	}
	if !strings.HasSuffix(path, ".zip") {
		t.Errorf("BackupPath() = %q, want suffix .zip", path)
	}
}

func TestBackupPathTemplate(t *testing.T) {
	m := New("/tmp/backups")
	path := m.BackupPath("pihole", "pihole", ".zip", "pihole_backup_{2006-01-02}.zip")

	today := time.Now().Format("2006-01-02")
	want := "/tmp/backups/pihole/pihole_backup_" + today + ".zip"
	if path != want {
		t.Errorf("BackupPath() = %q, want %q", path, want)
	}
}

func TestBackupPathTemplateMultiplePlaceholders(t *testing.T) {
	m := New("/tmp/backups")
	path := m.BackupPath("ha", "ha", ".tar", "ha-{2006}-{01}-{02}.tar")

	now := time.Now()
	want := filepath.Join("/tmp/backups/ha", "ha-"+now.Format("2006")+"-"+now.Format("01")+"-"+now.Format("02")+".tar")
	if path != want {
		t.Errorf("BackupPath() = %q, want %q", path, want)
	}
}

func TestRecordAndLoadLog(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	rec := BackupRecord{
		Name:      "test",
		Filename:  "test-20260315-060000.tar.gz",
		Timestamp: time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC),
		Size:      1024,
	}

	if err := m.RecordBackup(rec); err != nil {
		t.Fatalf("RecordBackup() error: %v", err)
	}

	log, err := m.LoadLog()
	if err != nil {
		t.Fatalf("LoadLog() error: %v", err)
	}

	if len(log.Records) != 1 {
		t.Fatalf("len(Records) = %d, want 1", len(log.Records))
	}

	got := log.Records[0]
	if got.Name != "test" {
		t.Errorf("Name = %q, want %q", got.Name, "test")
	}
	if got.Size != 1024 {
		t.Errorf("Size = %d, want 1024", got.Size)
	}
	if got.Error != "" {
		t.Errorf("Error = %q, want empty", got.Error)
	}
}

func TestRecordBackupWithError(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	rec := BackupRecord{
		Name:      "failed",
		Timestamp: time.Now(),
		Error:     "connection refused",
	}

	if err := m.RecordBackup(rec); err != nil {
		t.Fatalf("RecordBackup() error: %v", err)
	}

	log, err := m.LoadLog()
	if err != nil {
		t.Fatalf("LoadLog() error: %v", err)
	}

	if log.Records[0].Error != "connection refused" {
		t.Errorf("Error = %q, want %q", log.Records[0].Error, "connection refused")
	}
}

func TestLoadLogEmpty(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	log, err := m.LoadLog()
	if err != nil {
		t.Fatalf("LoadLog() error: %v", err)
	}
	if len(log.Records) != 0 {
		t.Errorf("len(Records) = %d, want 0", len(log.Records))
	}
}

func TestMultipleRecords(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	for i := range 3 {
		rec := BackupRecord{
			Name:      "test",
			Filename:  "file.tar.gz",
			Timestamp: time.Now().Add(time.Duration(i) * time.Hour),
			Size:      int64(i * 100),
		}
		if err := m.RecordBackup(rec); err != nil {
			t.Fatalf("RecordBackup() error: %v", err)
		}
	}

	log, err := m.LoadLog()
	if err != nil {
		t.Fatalf("LoadLog() error: %v", err)
	}
	if len(log.Records) != 3 {
		t.Errorf("len(Records) = %d, want 3", len(log.Records))
	}
}

func TestEnforceRetention(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	backupDir := filepath.Join(dir, "test")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create 5 files with staggered mod times
	for i := range 5 {
		path := filepath.Join(backupDir, "backup-"+string(rune('a'+i))+".tar.gz")
		if err := os.WriteFile(path, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		modTime := time.Now().Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
	}

	if err := m.EnforceRetention("test", 2); err != nil {
		t.Fatalf("EnforceRetention() error: %v", err)
	}

	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("after retention: %d files, want 2", len(entries))
	}

	// The two newest (d, e) should remain
	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["backup-d.tar.gz"] || !names["backup-e.tar.gz"] {
		t.Errorf("expected newest files to survive, got: %v", names)
	}
}

func TestEnforceRetentionEmptyDir(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	// Should not error on nonexistent directory
	if err := m.EnforceRetention("nonexistent", 2); err != nil {
		t.Errorf("EnforceRetention() unexpected error: %v", err)
	}
}

func TestListBackups(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	backupDir := filepath.Join(dir, "pihole")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a.zip", "b.zip", "c.zip"} {
		if err := os.WriteFile(filepath.Join(backupDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := m.ListBackups("pihole")
	if err != nil {
		t.Fatalf("ListBackups() error: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("len(entries) = %d, want 3", len(entries))
	}
}

func TestListBackupsNonexistent(t *testing.T) {
	dir := t.TempDir()
	m := New(dir)

	entries, err := m.ListBackups("nonexistent")
	if err != nil {
		t.Fatalf("ListBackups() error: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil entries, got %d", len(entries))
	}
}
