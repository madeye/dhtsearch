#!/usr/bin/env bash
# sync-to-remote.sh — incrementally merge the local DHT crawler's index into
# the remote server's database.
#
# How it works: export torrents rows newer than the last-sync watermark into a
# small delta SQLite file, ship it to the server, and merge it with
# INSERT OR IGNORE (dedup by info_hash primary key). Only the delta crosses
# the network.
#
# The remote host is NOT stored here. It comes from $DEPLOY_HOST or a
# gitignored .deploy.env, looked up in the repo root and then in the state dir:
#   echo 'DEPLOY_HOST=user@example.com' > .deploy.env
#
# The crawler's database lives in the state dir rather than the checkout, so
# the launchd agent can reach it — see scripts/launchd/install.sh. Override
# with DHTSEARCH_STATE_DIR, or point LOCAL_DB/WATERMARK_FILE somewhere else
# outright.
#
# Optional: DEPLOY_DIR (default /opt/dhtsearch), SYNC_INTERVAL is controlled
# by the scheduler (launchd/cron), not this script.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="${DHTSEARCH_STATE_DIR:-$HOME/Library/Application Support/dhtsearch}"
for f in "$ROOT_DIR/.deploy.env" "$STATE_DIR/.deploy.env"; do
    # `if` rather than `[ ... ] && .` so a miss on the last candidate does not
    # make the loop exit non-zero and trip `set -e`.
    if [ -f "$f" ]; then . "$f"; break; fi
done

DEPLOY_HOST="${DEPLOY_HOST:?DEPLOY_HOST is required (env or .deploy.env)}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/dhtsearch}"
LOCAL_DB="${LOCAL_DB:-$STATE_DIR/local.db}"
WATERMARK_FILE="${WATERMARK_FILE:-$STATE_DIR/last_sync_ts}"
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
#
# Columns are listed explicitly rather than SELECT *: the delta schema then
# stays stable even when the two ends run different binaries, and reviewed_at
# is deliberately left behind so the remote applies its own moderation policy
# to synced rows.
"$SQLITE" "$LOCAL_DB" <<SQL
PRAGMA busy_timeout=5000;
ATTACH '$DELTA' AS d;
CREATE TABLE d.torrents AS
  SELECT info_hash, name, total_size, file_count, files_json, created_at
  FROM torrents WHERE created_at > $LAST;
SQL

COUNT=$("$SQLITE" "$DELTA" "SELECT COUNT(*) FROM torrents;")
if [ "$COUNT" = "0" ]; then
    echo "nothing new to sync"
    exit 0
fi
NEW_MARK=$("$SQLITE" "$DELTA" "SELECT MAX(created_at) FROM torrents;")

REMOTE_TMP=$(ssh "$DEPLOY_HOST" "mktemp /tmp/dhtsearch-sync.XXXXXX.db")
scp -q "$DELTA" "$DEPLOY_HOST:$REMOTE_TMP"

# Rows the remote's moderation pass already rejected must not come back.
ssh "$DEPLOY_HOST" "$SQLITE '$DEPLOY_DIR/data/dhtsearch.db' \"
PRAGMA busy_timeout=10000;
ATTACH '$REMOTE_TMP' AS d;
INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files_json, created_at)
  SELECT info_hash, name, total_size, file_count, files_json, created_at FROM d.torrents
  WHERE info_hash NOT IN (SELECT info_hash FROM blocked);
\" && rm -f '$REMOTE_TMP'"

echo "$NEW_MARK" > "$WATERMARK_FILE"
echo "synced $COUNT torrents (watermark: $LAST -> $NEW_MARK)"
