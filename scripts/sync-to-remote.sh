#!/usr/bin/env bash
# sync-to-remote.sh — incrementally merge the local DHT crawler's index into
# the remote server's database.
#
# How it works: export torrents rows newer than the last-sync watermark into a
# small delta SQLite file, ship it to the server, and merge it with
# INSERT OR IGNORE (dedup by info_hash primary key). Only the delta crosses
# the network.
#
# The remote host is NOT stored here. It comes from $DEPLOY_HOST or the
# gitignored .deploy.env file in the repo root:
#   echo 'DEPLOY_HOST=user@example.com' > .deploy.env
#
# Optional: DEPLOY_DIR (default /opt/dhtsearch), SYNC_INTERVAL is controlled
# by the scheduler (launchd/cron), not this script.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[ -f "$ROOT_DIR/.deploy.env" ] && . "$ROOT_DIR/.deploy.env"

DEPLOY_HOST="${DEPLOY_HOST:?DEPLOY_HOST is required (env or .deploy.env)}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/dhtsearch}"
LOCAL_DB="$ROOT_DIR/data/local.db"
WATERMARK_FILE="$ROOT_DIR/data/last_sync_ts"
SQLITE=/usr/bin/sqlite3

[ -f "$LOCAL_DB" ] || { echo "local db not found: $LOCAL_DB"; exit 1; }

LAST=0
[ -f "$WATERMARK_FILE" ] && LAST=$(cat "$WATERMARK_FILE")

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT
DELTA="$TMP_DIR/delta.db"

# Export rows newer than the watermark. A single query is a consistent
# snapshot; the new watermark is taken from the delta itself, so rows written
# during the export are picked up by the next run instead of being skipped.
"$SQLITE" "$LOCAL_DB" <<SQL
PRAGMA busy_timeout=5000;
ATTACH '$DELTA' AS d;
CREATE TABLE d.torrents AS SELECT * FROM torrents WHERE created_at > $LAST;
SQL

COUNT=$("$SQLITE" "$DELTA" "SELECT COUNT(*) FROM torrents;")
if [ "$COUNT" = "0" ]; then
    echo "nothing new to sync"
    exit 0
fi
NEW_MARK=$("$SQLITE" "$DELTA" "SELECT MAX(created_at) FROM torrents;")

REMOTE_TMP=$(ssh "$DEPLOY_HOST" "mktemp /tmp/dhtsearch-sync.XXXXXX.db")
scp -q "$DELTA" "$DEPLOY_HOST:$REMOTE_TMP"

ssh "$DEPLOY_HOST" "$SQLITE '$DEPLOY_DIR/data/dhtsearch.db' \"
PRAGMA busy_timeout=10000;
ATTACH '$REMOTE_TMP' AS d;
INSERT OR IGNORE INTO torrents SELECT * FROM d.torrents;
\" && rm -f '$REMOTE_TMP'"

echo "$NEW_MARK" > "$WATERMARK_FILE"
echo "synced $COUNT torrents (watermark: $LAST -> $NEW_MARK)"
