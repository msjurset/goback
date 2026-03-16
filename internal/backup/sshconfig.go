package backup

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

type sshHostConfig struct {
	Hostname      string
	User          string
	Port          string
	IdentityAgent string
}

// resolveSSHConfig parses ~/.ssh/config to resolve Host aliases to their
// actual Hostname, User, Port, and IdentityAgent values. It merges
// settings from matching Host blocks, with more specific blocks taking
// precedence (later values don't override earlier ones, matching OpenSSH
// behavior where first match wins per-field). The Host * block provides
// defaults for all connections.
func resolveSSHConfig(alias string) *sshHostConfig {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	f, err := os.Open(filepath.Join(home, ".ssh", "config"))
	if err != nil {
		return nil
	}
	defer f.Close()

	result := &sshHostConfig{}
	var matched bool
	var anyMatch bool

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, val, ok := parseSSHConfigLine(line)
		if !ok {
			continue
		}

		switch strings.ToLower(key) {
		case "host":
			matched = matchHost(val, alias)
			if matched {
				anyMatch = true
			}
		case "hostname":
			if matched && result.Hostname == "" {
				result.Hostname = val
			}
		case "user":
			if matched && result.User == "" {
				result.User = val
			}
		case "port":
			if matched && result.Port == "" {
				result.Port = val
			}
		case "identityagent":
			if matched && result.IdentityAgent == "" {
				// Strip surrounding quotes and expand ~
				val = strings.Trim(val, "\"")
				if strings.HasPrefix(val, "~/") {
					val = filepath.Join(home, val[2:])
				}
				result.IdentityAgent = val
			}
		}
	}

	if !anyMatch {
		return nil
	}
	return result
}

func parseSSHConfigLine(line string) (key, value string, ok bool) {
	// Handle both "Key Value" and "Key=Value" formats
	if idx := strings.IndexByte(line, '='); idx > 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) != 2 {
		parts = strings.SplitN(line, "\t", 2)
	}
	if len(parts) != 2 {
		return "", "", false
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), true
}

func matchHost(pattern, alias string) bool {
	for _, p := range strings.Fields(pattern) {
		if p == "*" || p == alias {
			return true
		}
	}
	return false
}
