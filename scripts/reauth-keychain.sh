#!/usr/bin/env bash
# Re-authorize goback keychain items.
#
# Run this when scheduled (non-interactive) invocations of `goback run`
# start failing with "keychain store: Write permissions error" — typically
# after a macOS upgrade, after re-deploying the goback binary, or when a
# keychain item was originally created by a different binary whose ACL
# doesn't include the current goback.
#
# What it does:
#   1. Clears all cached goback secrets from the login keychain.
#   2. Re-runs `goback auth` which resolves op:// references via 1Password
#      and re-caches them. The new items are written with -T ACL entries
#      that trust the goback binary for non-interactive reads.
#
# 1Password authentication will be required during step 2.

set -euo pipefail

GOBACK_BIN="${GOBACK_BIN:-/usr/local/bin/goback}"

if [[ ! -x "$GOBACK_BIN" ]]; then
    echo "error: goback binary not found at $GOBACK_BIN" >&2
    echo "hint: set GOBACK_BIN=<path>, or run 'make deploy' first" >&2
    exit 1
fi

echo "==> goback binary: $GOBACK_BIN"
"$GOBACK_BIN" --version
echo

echo "==> Clearing existing goback keychain items..."
"$GOBACK_BIN" auth --clear

echo
echo "==> Re-resolving secrets from 1Password and caching to keychain..."
echo "    (you may be prompted to authenticate with 1Password)"
"$GOBACK_BIN" auth

echo
echo "==> Done. Keychain items now trust $GOBACK_BIN for non-interactive reads."
echo "    Verify with: goback run <name>  (from a cron/launchd context)"
