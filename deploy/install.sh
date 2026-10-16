#!/bin/sh
# Install the bridge as a systemd *user* service (no root needed).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
if [ ! -x "$here/../bridge/wareader-bridge" ]; then
  echo "bridge/wareader-bridge is missing: build it with 'cd bridge && CGO_ENABLED=0 go build' (on any machine) and copy it here" >&2
  exit 1
fi
mkdir -p ~/.config/systemd/user
ln -sf "$here/wareader-bridge.service" ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable wareader-bridge.service
# Keep user services running when you're not logged in.
loginctl enable-linger "$(id -un)"
if [ -f "$here/../data/session.db" ]; then
  systemctl --user restart wareader-bridge.service
else
  echo "Not linked yet. Run: uv run wareader link   (then the service starts itself)"
fi
