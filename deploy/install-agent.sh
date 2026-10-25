#!/bin/sh
# Install `whatsapp-reader agent` as a systemd user service next to the bridge (no root needed).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
grep -q '^\[agent\]' "$here/../config.toml" || { echo "config.toml has no [agent] section" >&2; exit 1; }
for bin in claude gh uv; do command -v "$bin" >/dev/null || { echo "$bin not found in PATH" >&2; exit 1; }; done
mkdir -p ~/.config/systemd/user
ln -sf "$here/whatsapp-reader-agent.service" ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now whatsapp-reader-agent.service
systemctl --user --no-pager status whatsapp-reader-agent.service | head -5
