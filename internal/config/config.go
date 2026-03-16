package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/msjurset/goback/internal/credentials"
)

type Config struct {
	Storage StorageConfig  `yaml:"storage"`
	Backups []BackupConfig `yaml:"backups"`
}

type StorageConfig struct {
	BaseDir string `yaml:"base_dir"`
	LogFile string `yaml:"log_file"`
}

type BackupConfig struct {
	Name        string `yaml:"name"`
	Type        string `yaml:"type"`
	Schedule    string `yaml:"schedule"`
	Folder      string `yaml:"folder,omitempty"`
	Filename    string `yaml:"filename,omitempty"`
	Retention   int    `yaml:"retention"`
	HAUrl       string `yaml:"ha_url,omitempty"`
	HAToken     string `yaml:"ha_token,omitempty"`
	Host        string `yaml:"host,omitempty"`
	User        string `yaml:"user,omitempty"`
	SSHKey      string `yaml:"ssh_key,omitempty"`
	PreCommand    string `yaml:"pre_command,omitempty"`
	RemotePath    string `yaml:"remote_path,omitempty"`
	RemotePattern string `yaml:"remote_pattern,omitempty"`
	PostCommand   string `yaml:"post_command,omitempty"`
}

func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "goback", "config.yaml")
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	cfg.Storage.BaseDir = expandPath(cfg.Storage.BaseDir)
	cfg.Storage.LogFile = expandPath(cfg.Storage.LogFile)

	if err := cfg.resolveSecrets(); err != nil {
		return nil, fmt.Errorf("resolving secrets: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// LoadRaw parses the config file without resolving secrets or validating.
// Used by the auth command to read op:// references before they're resolved.
func LoadRaw(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	return &cfg, nil
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func (c *Config) validate() error {
	if c.Storage.BaseDir == "" {
		return fmt.Errorf("storage.base_dir is required")
	}

	seen := make(map[string]bool)
	for i, b := range c.Backups {
		if b.Name == "" {
			return fmt.Errorf("backup[%d]: name is required", i)
		}
		if seen[b.Name] {
			return fmt.Errorf("backup %q: duplicate name", b.Name)
		}
		seen[b.Name] = true

		if b.Schedule == "" {
			return fmt.Errorf("backup %q: schedule is required", b.Name)
		}
		if b.Folder == "" {
			c.Backups[i].Folder = b.Name
		}
		if b.Retention <= 0 {
			c.Backups[i].Retention = 4
		}

		switch b.Type {
		case "ha_api":
			if b.HAUrl == "" {
				return fmt.Errorf("backup %q: ha_url is required for ha_api type", b.Name)
			}
			if b.HAToken == "" {
				return fmt.Errorf("backup %q: ha_token is required for ha_api type", b.Name)
			}
		case "ssh":
			if b.Host == "" {
				return fmt.Errorf("backup %q: host is required for ssh type", b.Name)
			}
			if b.User == "" {
				return fmt.Errorf("backup %q: user is required for ssh type", b.Name)
			}
			if b.RemotePath == "" && b.RemotePattern == "" {
				return fmt.Errorf("backup %q: remote_path or remote_pattern is required for ssh type", b.Name)
			}
		default:
			return fmt.Errorf("backup %q: unknown type %q (expected ha_api or ssh)", b.Name, b.Type)
		}
	}

	return nil
}

func (c *Config) resolveSecrets() error {
	for i, b := range c.Backups {
		if strings.HasPrefix(b.HAToken, "op://") {
			key := "ha_token_" + b.Name
			val, err := credentials.LoadOrResolve(key, b.HAToken)
			if err != nil {
				return fmt.Errorf("backup %q ha_token: %w", b.Name, err)
			}
			c.Backups[i].HAToken = val
		}
		if strings.HasPrefix(b.SSHKey, "op://") {
			key := "ssh_key_" + b.Name
			val, err := credentials.LoadOrResolve(key, b.SSHKey)
			if err != nil {
				return fmt.Errorf("backup %q ssh_key: %w", b.Name, err)
			}
			c.Backups[i].SSHKey = val
		}
	}
	return nil
}

func DefaultConfig() string {
	return `storage:
  base_dir: ~/backups
  log_file: ~/Library/Logs/goback.log

backups:
  - name: homeassistant
    type: ha_api
    schedule: "0 6 * * 0"    # Sunday 6:00 AM
    folder: homeassistant
    ha_url: http://homeassistant.local:8123
    ha_token: "op://Vault/HomeAssistant/token"
    retention: 4

  - name: pihole
    type: ssh
    schedule: "1 3 * * 0"    # Sunday 3:01 AM
    folder: pihole
    filename: "pihole_backup_{2006-01-02}.zip"
    host: pi-hole
    user: pi
    remote_path: /home/pi/backups/pihole/pihole_backup.zip
    retention: 4

  - name: pivpn
    type: ssh
    schedule: "0 7 * * 0"
    folder: pivpn
    host: pihole.local
    user: pi
    pre_command: "sudo tar czf /tmp/pivpn-backup.tar.gz /etc/pivpn /etc/wireguard"
    remote_path: /tmp/pivpn-backup.tar.gz
    post_command: "rm /tmp/pivpn-backup.tar.gz"
    retention: 4

  - name: unbound
    type: ssh
    schedule: "0 7 * * 0"
    folder: unbound
    host: pihole.local
    user: pi
    pre_command: "sudo tar czf /tmp/unbound-backup.tar.gz /etc/unbound"
    remote_path: /tmp/unbound-backup.tar.gz
    post_command: "rm /tmp/unbound-backup.tar.gz"
    retention: 4
`
}
