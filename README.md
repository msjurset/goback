# goback

Scheduled pull-based backup manager for home network services.

## Features

- **Home Assistant API backups** — triggers backup creation via REST, lists/downloads via WebSocket API, fetches via signed URLs
- **SSH/SCP backups** — pulls files from remote hosts via SSH agent or per-backup SSH keys
- **Local backups** — runs a local command to generate a backup file, then copies it into managed storage
- **Cron scheduling** — each backup job runs on its own cron schedule
- **Retention management** — automatically removes old backups beyond configured count
- **1Password integration** — resolves `op://` secret references for tokens and SSH keys
- **Platform keychain caching** — `goback auth` resolves secrets and caches them in the system keychain (macOS Keychain, Linux secret-tool, Windows cmdkey) so the daemon runs without 1Password
- **SSH config parsing** — reads `~/.ssh/config` for Host aliases, Hostname, Port, User, and IdentityAgent
- **PKCS#8 SSH key support** — handles 1Password's SSH key export format
- **Glob-based remote files** — `remote_pattern` finds the newest file matching a glob on the remote host
- **Compound extension handling** — preserves `.tar.gz` and similar multi-part extensions
- **Dry run mode** — validates connectivity and config without transferring files
- **Configurable filenames** — Go time format templates for output file naming
- **Missed backup catchup** — detects backups missed during sleep/downtime and runs them on wake
- **macOS launchd service** — runs as a user-level daemon with auto-restart

## Install

```
make deploy
```

This builds the binary to `/usr/local/bin/`, installs the man page, and sets up zsh completions.

## Usage

```
goback <command> [args]
```

### Commands

| Command | Description |
|---------|-------------|
| `init` | Create default config file at `~/.config/goback/config.yaml` |
| `auth` | Resolve `op://` secrets and cache them in the platform keychain |
| `auth --clear` | Remove cached secrets from the platform keychain |
| `clear [name]` | Remove cached secrets from keychain (all if no name given) |
| `daemon` | Run the backup scheduler in foreground with missed backup catchup |
| `run [name]` | Manually trigger one or all backups |
| `now` | Run all backups immediately |
| `dry-run [name]` | Simulate backups — connect but don't transfer |
| `list` | Show configured backup jobs |
| `status` | Show recent backup history |
| `last <name>` | Print timestamp of last successful backup for a job (exits 1 if none) |
| `version` | Print version (also `-v`, `--version`) |

### Examples

```bash
# Initialize config
goback init

# Resolve 1Password secrets and cache in system keychain
goback auth

# Clear cached secrets from keychain
goback auth --clear

# Clear all cached secrets (same as auth --clear)
goback clear

# Clear only homeassistant secrets
goback clear homeassistant

# Verify all targets are reachable
goback dry-run

# Test just the pihole backup
goback dry-run pihole

# Manually run a single backup
goback run pihole

# Run all backups now
goback now

# Show what's configured
goback list

# Check recent backup results
goback status

# Print version
goback version

# Start the daemon (or let launchd do it)
goback daemon
```

### Configuration

Config lives at `~/.config/goback/config.yaml`:

```yaml
storage:
  base_dir: ~/backups
  log_file: ~/Library/Logs/goback.log

backups:
  - name: homeassistant
    type: ha_api
    schedule: "0 6 * * 0"
    folder: homeassistant
    ha_url: http://homeassistant.local:8123
    ha_token: "op://Vault/HomeAssistant/token"
    retention: 4

  - name: pihole
    type: ssh
    schedule: "1 3 * * 0"
    folder: pihole
    filename: "pihole_backup_{2006-01-02}.zip"
    host: pi-hole
    user: pi
    remote_path: /home/pi/backups/pihole/pihole_backup.zip
    retention: 4

  - name: unbound
    type: ssh
    schedule: "2 3 * * 0"
    folder: unbound
    host: pi-hole
    user: pi
    ssh_key: "op://Vault/SSH Key/private key"
    remote_pattern: "/home/pi/backups/unbound/unbound-*.tar.gz"
    retention: 4

  - name: recruit
    type: local
    schedule: "0 5 * * 0"
    folder: recruit
    pre_command: "recruit --backup"
    local_path: /tmp/recruit-backup.tar.gz
    post_command: "rm /tmp/recruit-backup.tar.gz"
    retention: 4
```

#### Backup Config Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Unique identifier for this backup |
| `type` | string | yes | `ha_api`, `ssh`, or `local` |
| `schedule` | string | yes | Cron expression (5-field) |
| `folder` | string | no | Subdirectory in base_dir (defaults to name) |
| `filename` | string | no | Output filename template with `{time-format}` |
| `retention` | int | no | Backups to keep (default: 4) |
| `ha_url` | string | ha_api | Home Assistant base URL |
| `ha_token` | string | ha_api | API token (supports `op://` references) |
| `host` | string | ssh | SSH hostname or alias (reads `~/.ssh/config`) |
| `user` | string | ssh | SSH username |
| `remote_path` | string | ssh | File to download |
| `remote_pattern` | string | ssh | Glob pattern to find newest matching remote file (alternative to `remote_path`) |
| `ssh_key` | string | no | SSH private key for this backup; supports `op://` references |
| `pre_command` | string | no | Command before download (remote for ssh, local for local) |
| `post_command` | string | no | Command after download (remote for ssh, local for local) |
| `local_path` | string | local | Local file to back up |
| `local_pattern` | string | local | Glob pattern to find newest matching local file (alternative to `local_path`) |

### Authentication & Keychain

`goback auth` resolves all `op://` references in the config (API tokens, SSH keys) via the 1Password CLI and caches the resolved values in the platform keychain:

- **macOS** — Keychain (via `security`)
- **Linux** — Secret Service / `secret-tool`
- **Windows** — Windows Credential Manager / `cmdkey`

This lets the daemon run unattended without requiring 1Password to be unlocked. Run `goback auth` once after changing secrets, then start the daemon. Use `goback auth --clear` to remove all cached secrets.

### Service Management

```bash
# Start daemon via launchd
launchctl load ~/Library/LaunchAgents/com.goback.daemon.plist

# Stop daemon
launchctl unload ~/Library/LaunchAgents/com.goback.daemon.plist
```

## Build

```
make build
```

## License

MIT
