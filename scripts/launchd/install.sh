#!/usr/bin/env bash
# Install (or update) the launchd agents for the local crawler + periodic sync.
#
# Re-run this after pulling new code: it rebuilds the binary, refreshes the
# installed scripts and reloads both agents.
#
# Everything the agents touch at runtime — binary, database, logs, scripts —
# is installed under STATE_DIR/LOG_DIR, deliberately NOT in the checkout.
# launchd agents get no TCC access to non-system volumes, so a checkout on an
# external disk (/Volumes/...) is unreadable and unwritable to them: the job
# dies at spawn with EX_CONFIG (78), or every file operation fails with
# "Operation not permitted". Paths under $HOME need no permission grant.
#
#   DHTSEARCH_STATE_DIR   override the state dir (default below)
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STATE_DIR="${DHTSEARCH_STATE_DIR:-$HOME/Library/Application Support/dhtsearch}"
LOG_DIR="$HOME/Library/Logs/dhtsearch"
AGENT_DIR="$HOME/Library/LaunchAgents"

mkdir -p "$STATE_DIR/bin" "$LOG_DIR" "$AGENT_DIR"

echo "==> Building crawler binary into $STATE_DIR/bin"
(cd "$ROOT_DIR/server" && go build -o "$STATE_DIR/bin/dhtsearch-server" ./cmd/server)

echo "==> Installing sync script"
install -m 755 "$ROOT_DIR/scripts/sync-to-remote.sh" "$STATE_DIR/bin/sync-to-remote.sh"

# The sync job reads DEPLOY_HOST from .deploy.env in its state dir, so the
# gitignored copy in the checkout has to come along (the agent cannot read the
# checkout). Refreshed on every run so edits in the repo take effect.
if [ -f "$ROOT_DIR/.deploy.env" ]; then
    install -m 600 "$ROOT_DIR/.deploy.env" "$STATE_DIR/.deploy.env"
else
    echo "    note: no .deploy.env in the checkout — the sync agent will fail" \
         "until $STATE_DIR/.deploy.env exists (echo 'DEPLOY_HOST=user@host' > ...)"
fi

# One-time migration from the old in-checkout layout.
for f in local.db last_sync_ts; do
    if [ -f "$ROOT_DIR/data/$f" ] && [ ! -e "$STATE_DIR/$f" ]; then
        cp -p "$ROOT_DIR/data/$f" "$STATE_DIR/$f"
        echo "    migrated data/$f -> $STATE_DIR/$f"
    fi
done

echo "==> Loading agents"
for name in com.dhtsearch.crawler com.dhtsearch.sync; do
    sed -e "s|__STATE_DIR__|$STATE_DIR|g" -e "s|__LOG_DIR__|$LOG_DIR|g" \
        "$ROOT_DIR/scripts/launchd/$name.plist" > "$AGENT_DIR/$name.plist"
    launchctl unload "$AGENT_DIR/$name.plist" 2>/dev/null || true
    launchctl load -w "$AGENT_DIR/$name.plist"
    echo "    installed $name"
done

echo "==> Done. Crawler API on http://127.0.0.1:8089, logs in $LOG_DIR"
