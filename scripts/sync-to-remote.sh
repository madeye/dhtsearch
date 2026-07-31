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
CATEGORY_WATERMARK_FILE="${CATEGORY_WATERMARK_FILE:-${WATERMARK_FILE}.categories}"
SQLITE=/usr/bin/sqlite3

[ -f "$LOCAL_DB" ] || { echo "local db not found: $LOCAL_DB"; exit 1; }

LAST=0
[ -f "$WATERMARK_FILE" ] && LAST=$(cat "$WATERMARK_FILE")
CATEGORY_LAST=0
[ -f "$CATEGORY_WATERMARK_FILE" ] && CATEGORY_LAST=$(cat "$CATEGORY_WATERMARK_FILE")

TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT
DELTA="$TMP_DIR/delta.db"

# Export rows newer than the watermark. A single query is a consistent
# snapshot; the new watermark is taken from the delta itself, so rows written
# during the export are picked up by the next run instead of being skipped.
#
# Columns are listed explicitly rather than SELECT *: the delta schema then
# stays stable even when the two ends run different binaries. Category rows are
# copied separately so adult classifications cannot turn into general results
# after a sync.
"$SQLITE" "$LOCAL_DB" <<SQL
PRAGMA busy_timeout=5000;
ATTACH '$DELTA' AS d;
CREATE TABLE d.torrents AS
  SELECT info_hash, name, total_size, file_count, files, created_at
  FROM torrents WHERE created_at > $LAST;
CREATE TABLE d.categories AS
  SELECT c.info_hash, c.category, c.confidence, c.reason, c.reviewed_at
  FROM categories c JOIN torrents t ON t.info_hash = c.info_hash
  WHERE t.created_at > $LAST OR c.reviewed_at > $CATEGORY_LAST;
SQL

COUNT=$("$SQLITE" "$DELTA" "SELECT COUNT(*) FROM torrents;")
CATEGORY_COUNT=$("$SQLITE" "$DELTA" "SELECT COUNT(*) FROM categories;")
if [ "$COUNT" = "0" ] && [ "$CATEGORY_COUNT" = "0" ]; then
    echo "nothing new to sync"
    exit 0
fi
NEW_MARK=$("$SQLITE" "$DELTA" "SELECT COALESCE(MAX(created_at), $LAST) FROM torrents;")
NEW_CATEGORY_MARK=$("$SQLITE" "$DELTA" "SELECT COALESCE(MAX(reviewed_at), $CATEGORY_LAST) FROM categories;")

REMOTE_TMP=$(ssh "$DEPLOY_HOST" "mktemp /tmp/dhtsearch-sync.XXXXXX.db")
scp -q "$DELTA" "$DEPLOY_HOST:$REMOTE_TMP"

# Rows the remote's moderation pass already rejected must not come back.
ssh "$DEPLOY_HOST" "$SQLITE '$DEPLOY_DIR/data/dhtsearch.db' \"
PRAGMA busy_timeout=10000;
ATTACH '$REMOTE_TMP' AS d;
INSERT OR IGNORE INTO torrents (info_hash, name, total_size, file_count, files, created_at)
  SELECT info_hash, name, total_size, file_count, files, created_at FROM d.torrents
  WHERE info_hash NOT IN (SELECT info_hash FROM blocked);
INSERT INTO categories (info_hash, category, confidence, reason, reviewed_at)
  SELECT c.info_hash, c.category, c.confidence, c.reason, c.reviewed_at
  FROM d.categories c JOIN torrents t ON t.info_hash = c.info_hash
  WHERE 1
  ON CONFLICT(info_hash) DO UPDATE SET
    category = excluded.category,
    confidence = excluded.confidence,
    reason = excluded.reason,
    reviewed_at = excluded.reviewed_at
  WHERE excluded.reviewed_at >= categories.reviewed_at;
\" && rm -f '$REMOTE_TMP'"

echo "$NEW_MARK" > "$WATERMARK_FILE"
echo "$NEW_CATEGORY_MARK" > "$CATEGORY_WATERMARK_FILE"
echo "synced $COUNT torrents and $CATEGORY_COUNT categories (watermarks: $LAST -> $NEW_MARK, $CATEGORY_LAST -> $NEW_CATEGORY_MARK)"
