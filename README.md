# goback

Scheduled pull-based backup manager for home network services.

## Features

- **Home Assistant API backups** — triggers backup creation via REST, lists/downloads via WebSocket API, fetches via signed URLs
- **SSH/SCP backups** — pulls files from remote hosts via SSH agent or per-backup SSH keys; verifies host keys against `~/.ssh/known_hosts` with trust-on-first-use semantics
- **Local backups** — runs a local command to generate a backup file, then copies it into managed storage
- **Cron scheduling** — each backup job runs on its own cron schedule
- **Automatic retry with exponential backoff** — daemon-initiated runs retry transient failures (default 5 attempts: 30 s → 1 m → 2 m → 4 m → 8 m, with ±20 % jitter, capped at 15 m); each attempt is logged to `status.json`. Interactive `goback run`/`goback now` are single-attempt.
- **Atomic downloads** — files are written to `<dest>.partial` and renamed only after a clean transfer with a size match, so a truncated download never replaces a known-good backup
- **Failure notifications** — optional `notify_command` runs (per-backup or globally) when a backup fails after all retries; receives `GOBACK_BACKUP_NAME`, `GOBACK_BACKUP_TYPE`, `GOBACK_ERROR`, `GOBACK_ATTEMPTS` as env vars. Bounded by a 30 s timeout so a hung script can't stall the daemon.
- **Retention management** — automatically removes old backups beyond configured count
- **1Password integration** — resolves `op://` secret references for tokens and SSH keys
- **Platform keychain caching** — `goback auth` resolves secrets and caches them in the system keychain (macOS Keychain, Linux secret-tool, Windows cmdkey) so the daemon runs without 1Password. On macOS, cached items are created with ACL entries trusting the goback binary, so non-interactive callers (cron, launchd) can read them without a confirmation dialog.
- **SSH config parsing** — reads `~/.ssh/config` for Host aliases, Hostname, Port, User, and IdentityAgent
- **PKCS#8 SSH key support** — handles 1Password's SSH key export format
- **Glob-based remote files** — `remote_pattern` finds the newest file matching a glob on the remote host
- **Compound extension handling** — preserves `.tar.gz` and similar multi-part extensions
- **Dry run mode** — validates connectivity and config without transferring files
- **Configurable filenames** — Go time format templates for output file naming
- **Missed backup catchup** — detects backups missed during sleep/downtime and runs them on wake. The lookback window is sized from each backup's own schedule cycle (clamped to 7–90 days), so monthly and quarterly schedules are caught up too. Failure records count as attempts so a permanently-broken backup isn't re-fired by catchup every 5 minutes.
- **Config hot-reload** — daemon detects config file changes and rebuilds the schedule without restarting
- **Binary auto-restart** — daemon exits when the binary is updated, letting launchd restart with the new version
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
| `completion <shell>` | Output shell completion script (`bash`, `zsh`) |
| `daemon` | Run the backup scheduler in foreground with missed backup catchup |
| `run [--local] [name]` | Manually trigger one or all backups. Default delegates to the daemon over `~/.config/goback/control.sock`; `--local` runs in-process |
| `now [--local]` | Run all backups immediately. `--local` has the same meaning as on `run` |
| `dry-run [--local] [name]` | Simulate backups — connect but don't transfer. `--local` has the same meaning as on `run` |
| `list` | Show configured backup jobs |
| `status` | Show recent backup history |
| `last <name> [--epoch\|-e]` | Print timestamp of last successful backup (RFC 3339 by default; `--epoch` for Unix seconds, shell-script friendly) |
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

# Check when homeassistant was last backed up (human-readable)
goback last homeassistant

# Script-friendly epoch output (portable across BSD/GNU; no date parsing)
age_h=$(( ($(date +%s) - $(goback last homeassistant --epoch)) / 3600 ))
echo "HA last backup: ${age_h}h ago"

# Print version
goback version

# Install zsh completions
goback completion zsh > ~/.oh-my-zsh/custom/completions/_goback

# Enable bash completions for current session
eval "$(goback completion bash)"

# Start the daemon (or let launchd do it)
goback daemon
```

### Configuration

Config lives at `~/.config/goback/config.yaml`:

```yaml
storage:
  base_dir: ~/backups
  log_file: ~/Library/Logs/goback.log
  # records_per_backup: 200   # optional; default 200, set to -1 to disable

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
| `retry_max_attempts` | int | no | Override the default retry attempts (5) for this backup. Set to `1` to disable retries. |
| `notify_command` | string | no | Shell command to run on retry-exhausted failure for this backup. Overrides the global `notifications.failure_command`. Set to `" "` (single space) to explicitly opt out when a global default is set. |
| `insecure_skip_host_key` | bool | no | SSH only. Disables host key verification, matching pre-v1.7 behavior. Default `false`: keys are checked against `~/.ssh/known_hosts` with trust-on-first-use. |

#### Storage Config Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `base_dir` | string | yes | Where backup files and `status.json` live |
| `log_file` | string | no | Daemon log file path |
| `records_per_backup` | int | no | Per-backup-name cap on `status.json` history records. Default 200. Set to `-1` to disable the cap. |

#### Notifications

```yaml
notifications:
  failure_command: |
    osascript -e "display notification \"$GOBACK_BACKUP_NAME failed: $GOBACK_ERROR\" with title \"goback\""
```

When a backup fails after all retry attempts, goback runs `failure_command` via `sh -c` with these environment variables:

| Variable | Value |
|----------|-------|
| `GOBACK_BACKUP_NAME` | The `name` of the failed backup |
| `GOBACK_BACKUP_TYPE` | `ha_api`, `ssh`, or `local` |
| `GOBACK_ERROR` | The final error message |
| `GOBACK_ATTEMPTS` | How many attempts were made before giving up |

The command is bounded by a 30-second timeout. A non-zero exit is logged but does not affect the backup outcome. A per-backup `notify_command` overrides the global `failure_command`; set the per-backup field to `" "` (a single space) to opt that backup out when a global default is configured. Notifications are skipped on dry runs and on interactive `goback run`/`goback now` commands.

### SSH Host Key Verification

Starting with v1.7, SSH backups verify the remote host's key against `~/.ssh/known_hosts` using trust-on-first-use:

- **First connection to a host**: the key is appended to `known_hosts` and accepted; the SHA-256 fingerprint is logged so you can verify it.
- **Subsequent connections**: the key must match. A mismatch (host reinstalled, MITM, etc.) fails the backup with a clear error.
- **Skipping verification**: set `insecure_skip_host_key: true` on a backup to fall back to the pre-v1.7 ignore-host-key behavior. Use sparingly.

If `~/.ssh/known_hosts` doesn't exist, goback creates it (with `0o600` perms). Run `ssh <host>` once before scheduling a backup if you want to verify the key interactively before TOFU acceptance.

### Authentication & Keychain

`goback auth` resolves all `op://` references in the config (API tokens, SSH keys) via the 1Password CLI and caches the resolved values in the platform keychain:

- **macOS** — Keychain (via `security`)
- **Linux** — Secret Service / `secret-tool`
- **Windows** — Windows Credential Manager / `cmdkey`

This lets the daemon run unattended without requiring 1Password to be unlocked. Run `goback auth` once after changing secrets, then start the daemon. Use `goback auth --clear` to remove all cached secrets.

#### Why `goback run` delegates to the daemon

`goback run`, `goback now`, and `goback dry-run` route through the daemon's control socket (`~/.config/goback/control.sock`) by default. The reason is keychain reachability: cron- and `launchctl`-spawned processes live in a launchd session that does not carry the user's GUI keychain access, so `security find-generic-password` returns "no such item" even when the secret is present and the ACL would allow it. The daemon, started at user login as a LaunchAgent, holds resolved secrets in memory and runs in the user's GUI session, so it can execute backups without re-reading the keychain on every invocation. `goback run` from a cron line therefore "just works" without needing to re-resolve secrets in cron's reduced session.

`--local` opts out of delegation and runs the backup in-process. Use it when the daemon is not running (e.g., debugging the daemon itself) or when you want to test a specific binary's behavior. From cron and launchd, do not use `--local` — that's exactly the path that fails to read the keychain.

If scheduled (non-interactive) invocations of `goback run --local` start failing with `keychain store: Write permissions error` — typically after a macOS upgrade or when items were created by a different goback binary — run `make reauth-keychain` to clear and re-cache the items so the current binary is in the ACL. If goback is installed at more than one path, set `GOBACK_KEYCHAIN_TRUST=/path/to/other/goback` (colon-separated for multiple) when running `goback auth` so every invocation path is trusted. With the default daemon-delegated path, this class of error no longer applies — the daemon owns the keychain interaction, not the cron-spawned client.

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
