#!/usr/bin/env bash
# backup-r2.sh — snapshot the SQLite database and upload it to Cloudflare R2,
# keeping only the latest snapshot (one object, overwritten each run).
#
# Designed to run hourly on the server from a systemd timer (see
# scripts/systemd/). A raw copy of a live WAL database can be torn mid-write,
# so the snapshot is taken with VACUUM INTO — a consistent, compacted copy
# made under SQLite's own locking — then verified, compressed and uploaded.
# S3 object replacement is atomic: a failed or interrupted upload leaves the
# previous snapshot untouched, so there is never a window without a backup.
#
# No credentials live in this file. Everything comes from an env file
# (default /opt/dhtsearch/.backup.env, override with BACKUP_ENV_FILE):
#
#   R2_ACCOUNT_ID         (required) Cloudflare account id (hex, from the dash)
#   R2_ACCESS_KEY_ID      (required) R2 S3 API token key id
#   R2_SECRET_ACCESS_KEY  (required) R2 S3 API token secret
#   R2_BUCKET             (required) bucket name, e.g. dhtsearch-backup
#   DB_PATH               (optional) default /opt/dhtsearch/data/dhtsearch.db
#   BACKUP_OBJECT         (optional) object key, default dhtsearch-latest.db.zst
#
# Restore:
#   rclone copyto dhtr2:<bucket>/dhtsearch-latest.db.zst /tmp/x.db.zst
#   zstd -d /tmp/x.db.zst && systemctl stop dhtsearch-api \
#     && mv /tmp/x.db /opt/dhtsearch/data/dhtsearch.db \
#     && systemctl start dhtsearch-api

set -euo pipefail

ENV_FILE="${BACKUP_ENV_FILE:-/opt/dhtsearch/.backup.env}"
if [ ! -f "$ENV_FILE" ]; then
    echo "backup: env file $ENV_FILE not found" >&2
    exit 1
fi
# shellcheck source=/dev/null
. "$ENV_FILE"

: "${R2_ACCOUNT_ID:?R2_ACCOUNT_ID missing from $ENV_FILE}"
: "${R2_ACCESS_KEY_ID:?R2_ACCESS_KEY_ID missing from $ENV_FILE}"
: "${R2_SECRET_ACCESS_KEY:?R2_SECRET_ACCESS_KEY missing from $ENV_FILE}"
: "${R2_BUCKET:?R2_BUCKET missing from $ENV_FILE}"
DB_PATH="${DB_PATH:-/opt/dhtsearch/data/dhtsearch.db}"
BACKUP_OBJECT="${BACKUP_OBJECT:-dhtsearch-latest.db.zst}"

# rclone remote defined entirely via environment — no rclone.conf involvement,
# so this cannot collide with other remotes on the machine.
export RCLONE_CONFIG_DHTR2_TYPE=s3
export RCLONE_CONFIG_DHTR2_PROVIDER=Cloudflare
export RCLONE_CONFIG_DHTR2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export RCLONE_CONFIG_DHTR2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_DHTR2_ENDPOINT="https://${R2_ACCOUNT_ID}.r2.cloudflarestorage.com"
export RCLONE_CONFIG_DHTR2_ACL=private

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT
SNAP="$WORK_DIR/dhtsearch.db"

sqlite3 "$DB_PATH" "VACUUM INTO '$SNAP'"

# A backup that doesn't restore is noise with a false sense of safety.
CHECK="$(sqlite3 "$SNAP" "PRAGMA quick_check;")"
if [ "$CHECK" != "ok" ]; then
    echo "backup: snapshot failed quick_check: $CHECK" >&2
    exit 1
fi

zstd -q -T0 "$SNAP" -o "$SNAP.zst"

DEST="dhtr2:$R2_BUCKET/$BACKUP_OBJECT"
rclone copyto --s3-no-check-bucket "$SNAP.zst" "$DEST"

# Confirm the object is really there and fresh before declaring success.
AGE="$(rclone lsf --format tp "$DEST" | head -1)"
if [ -z "$AGE" ]; then
    echo "backup: uploaded object not visible in listing" >&2
    exit 1
fi

echo "backup: OK $DEST ($(du -h "$SNAP.zst" | cut -f1) compressed, remote: $AGE)"
