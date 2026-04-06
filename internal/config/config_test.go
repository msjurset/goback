package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPath(t *testing.T) {
	path := DefaultPath()
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "goback", "config.yaml")
	if path != want {
		t.Errorf("DefaultPath() = %q, want %q", path, want)
	}
}

func TestExpandPath(t *testing.T) {
	home, _ := os.UserHomeDir()

	tests := []struct {
		name string
		path string
		want string
	}{
		{"tilde prefix", "~/backups", filepath.Join(home, "backups")},
		{"tilde nested", "~/foo/bar/baz", filepath.Join(home, "foo/bar/baz")},
		{"absolute path", "/var/log/markback.log", "/var/log/markback.log"},
		{"relative path", "backups/data", "backups/data"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandPath(tt.path)
			if got != tt.want {
				t.Errorf("expandPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestLoadValidConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	yaml := `storage:
  base_dir: ` + dir + `/backups

backups:
  - name: test-ssh
    type: ssh
    schedule: "0 3 * * 0"
    host: example.local
    user: pi
    remote_path: /tmp/backup.tar.gz
    retention: 3

  - name: test-ha
    type: ha_api
    schedule: "0 6 * * 0"
    ha_url: http://ha.local:8123
    ha_token: fake-token
    retention: 5
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Storage.BaseDir != dir+"/backups" {
		t.Errorf("BaseDir = %q, want %q", cfg.Storage.BaseDir, dir+"/backups")
	}

	if len(cfg.Backups) != 2 {
		t.Fatalf("len(Backups) = %d, want 2", len(cfg.Backups))
	}

	ssh := cfg.Backups[0]
	if ssh.Name != "test-ssh" {
		t.Errorf("Backups[0].Name = %q, want %q", ssh.Name, "test-ssh")
	}
	if ssh.Folder != "test-ssh" {
		t.Errorf("Backups[0].Folder = %q, want %q (should default to name)", ssh.Folder, "test-ssh")
	}
	if ssh.Retention != 3 {
		t.Errorf("Backups[0].Retention = %d, want 3", ssh.Retention)
	}

	ha := cfg.Backups[1]
	if ha.HAToken != "fake-token" {
		t.Errorf("Backups[1].HAToken = %q, want %q", ha.HAToken, "fake-token")
	}
}

func TestLoadRetentionDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	yaml := `storage:
  base_dir: /tmp/backups

backups:
  - name: test
    type: ssh
    schedule: "0 3 * * 0"
    host: example.local
    user: pi
    remote_path: /tmp/backup.tar.gz
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Backups[0].Retention != 4 {
		t.Errorf("Retention = %d, want 4 (default)", cfg.Backups[0].Retention)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/config.yaml")
	if err == nil {
		t.Error("Load() expected error for missing file, got nil")
	}
}

func TestValidateMissingBaseDir(t *testing.T) {
	cfg := &Config{}
	err := cfg.validate()
	if err == nil {
		t.Error("validate() expected error for missing base_dir, got nil")
	}
}

func TestValidateMissingName(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{BaseDir: "/tmp"},
		Backups: []BackupConfig{{Type: "ssh", Schedule: "* * * * *", Host: "x", User: "x", RemotePath: "/x"}},
	}
	err := cfg.validate()
	if err == nil {
		t.Error("validate() expected error for missing name, got nil")
	}
}

func TestValidateDuplicateName(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{BaseDir: "/tmp"},
		Backups: []BackupConfig{
			{Name: "dup", Type: "ssh", Schedule: "* * * * *", Host: "x", User: "x", RemotePath: "/x"},
			{Name: "dup", Type: "ssh", Schedule: "* * * * *", Host: "y", User: "y", RemotePath: "/y"},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Error("validate() expected error for duplicate name, got nil")
	}
}

func TestValidateLocalValid(t *testing.T) {
	tests := []struct {
		name   string
		backup BackupConfig
	}{
		{"with local_path", BackupConfig{Name: "t", Type: "local", Schedule: "* * * * *", LocalPath: "/tmp/backup.tar.gz"}},
		{"with local_pattern", BackupConfig{Name: "t", Type: "local", Schedule: "* * * * *", LocalPattern: "/tmp/backup-*.tar.gz"}},
		{"with pre_command only", BackupConfig{Name: "t", Type: "local", Schedule: "* * * * *", PreCommand: "recruit --backup"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Storage: StorageConfig{BaseDir: "/tmp"},
				Backups: []BackupConfig{tt.backup},
			}
			if err := cfg.validate(); err != nil {
				t.Errorf("validate() unexpected error: %v", err)
			}
		})
	}
}

func TestValidateLocalMissingFields(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{BaseDir: "/tmp"},
		Backups: []BackupConfig{
			{Name: "t", Type: "local", Schedule: "* * * * *"},
		},
	}
	if err := cfg.validate(); err == nil {
		t.Error("validate() expected error for local type with no path/pattern/command, got nil")
	}
}

func TestValidateUnknownType(t *testing.T) {
	cfg := &Config{
		Storage: StorageConfig{BaseDir: "/tmp"},
		Backups: []BackupConfig{
			{Name: "bad", Type: "ftp", Schedule: "* * * * *"},
		},
	}
	err := cfg.validate()
	if err == nil {
		t.Error("validate() expected error for unknown type, got nil")
	}
}

func TestValidateSSHMissingFields(t *testing.T) {
	tests := []struct {
		name   string
		backup BackupConfig
	}{
		{"missing host", BackupConfig{Name: "t", Type: "ssh", Schedule: "* * * * *", User: "pi", RemotePath: "/x"}},
		{"missing user", BackupConfig{Name: "t", Type: "ssh", Schedule: "* * * * *", Host: "x", RemotePath: "/x"}},
		{"missing remote_path", BackupConfig{Name: "t", Type: "ssh", Schedule: "* * * * *", Host: "x", User: "pi"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Storage: StorageConfig{BaseDir: "/tmp"},
				Backups: []BackupConfig{tt.backup},
			}
			if err := cfg.validate(); err == nil {
				t.Error("validate() expected error, got nil")
			}
		})
	}
}

func TestValidateHAMissingFields(t *testing.T) {
	tests := []struct {
		name   string
		backup BackupConfig
	}{
		{"missing ha_url", BackupConfig{Name: "t", Type: "ha_api", Schedule: "* * * * *", HAToken: "tok"}},
		{"missing ha_token", BackupConfig{Name: "t", Type: "ha_api", Schedule: "* * * * *", HAUrl: "http://x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Storage: StorageConfig{BaseDir: "/tmp"},
				Backups: []BackupConfig{tt.backup},
			}
			if err := cfg.validate(); err == nil {
				t.Error("validate() expected error, got nil")
			}
		})
	}
}

func TestDefaultConfigIsValid(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(cfgPath, []byte(DefaultConfig()), 0o644); err != nil {
		t.Fatal(err)
	}

	// DefaultConfig uses op:// which would fail resolution, so we test
	// parsing only by replacing the op:// ref
	data, _ := os.ReadFile(cfgPath)
	patched := string(data)
	patched = replaceAll(patched, "op://Vault/HomeAssistant/token", "fake-token")
	if err := os.WriteFile(cfgPath, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(cfgPath)
	if err != nil {
		t.Errorf("DefaultConfig() produces invalid config: %v", err)
	}
}

func replaceAll(s, old, new string) string {
	for {
		i := indexOf(s, old)
		if i < 0 {
			return s
		}
		s = s[:i] + new + s[i+len(old):]
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
