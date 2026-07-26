#!/usr/bin/env bash
# deploy.sh — build and deploy DHTSearch (Go backend + Next.js frontend) to a
# remote Linux server over SSH.
#
# No hosts, domains, IPs, or credentials are stored in this file. Everything
# environment-specific comes from variables:
#
#   DEPLOY_HOST   (required) SSH target, e.g. "user@example.com".
#                 Use your ~/.ssh/config for aliases/keys — nothing sensitive
#                 needs to live here.
#   DEPLOY_DIR    (optional) remote install dir. Default: /opt/dhtsearch
#   API_PORT      (optional) backend port on the server, for the post-deploy
#                 health check. Default: 8081
#   GOARCH        (optional) remote CPU arch. Default: amd64
#
# Usage:
#   DEPLOY_HOST=user@example.com ./deploy.sh
#
# Prerequisites on the server (one-time setup, not covered by this script):
#   - systemd units dhtsearch-api.service and dhtsearch-web.service
#   - a reverse proxy (e.g. nginx) forwarding to the web service
#   - rsync, node

set -euo pipefail

DEPLOY_HOST="${DEPLOY_HOST:?DEPLOY_HOST is required, e.g. DEPLOY_HOST=user@example.com ./deploy.sh}"
DEPLOY_DIR="${DEPLOY_DIR:-/opt/dhtsearch}"
API_PORT="${API_PORT:-8081}"
GOARCH="${GOARCH:-amd64}"

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT

echo "==> Building backend (linux/$GOARCH)"
(cd "$ROOT_DIR/server" && GOOS=linux GOARCH="$GOARCH" go build -o "$BUILD_DIR/dhtsearch-server" ./cmd/server)

echo "==> Building frontend (standalone)"
(cd "$ROOT_DIR/web" && npm run build)
mkdir -p "$BUILD_DIR/web"
cp -R "$ROOT_DIR/web/.next/standalone/." "$BUILD_DIR/web/"
mkdir -p "$BUILD_DIR/web/.next"
cp -R "$ROOT_DIR/web/.next/static" "$BUILD_DIR/web/.next/static"
[ -d "$ROOT_DIR/web/public" ] && cp -R "$ROOT_DIR/web/public" "$BUILD_DIR/web/public"

echo "==> Uploading to $DEPLOY_HOST:$DEPLOY_DIR"
ssh "$DEPLOY_HOST" "mkdir -p '$DEPLOY_DIR/data' '$DEPLOY_DIR/web'"
rsync -az "$BUILD_DIR/dhtsearch-server" "$DEPLOY_HOST:$DEPLOY_DIR/server"
rsync -az --delete "$BUILD_DIR/web/" "$DEPLOY_HOST:$DEPLOY_DIR/web/"

echo "==> Restarting services"
ssh "$DEPLOY_HOST" "systemctl restart dhtsearch-api dhtsearch-web"

echo "==> Health check"
sleep 3
ssh "$DEPLOY_HOST" "curl -sf http://127.0.0.1:$API_PORT/api/healthz && echo && systemctl is-active dhtsearch-api dhtsearch-web"

echo "==> Deploy OK"
