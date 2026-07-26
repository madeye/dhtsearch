#!/usr/bin/env bash
# Install launchd agents for the local crawler + periodic sync (macOS).
set -euo pipefail
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
for name in com.dhtsearch.crawler com.dhtsearch.sync; do
    sed "s|__PROJECT_DIR__|$ROOT_DIR|g" "$ROOT_DIR/scripts/launchd/$name.plist" > "$HOME/Library/LaunchAgents/$name.plist"
    launchctl unload "$HOME/Library/LaunchAgents/$name.plist" 2>/dev/null || true
    launchctl load -w "$HOME/Library/LaunchAgents/$name.plist"
    echo "installed $name"
done
