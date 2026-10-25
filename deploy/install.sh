#!/bin/sh
# Install the bridge as a systemd *user* service (no root needed).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
if [ ! -x "$here/../bridge/whatsapp-reader-bridge" ]; then
  echo "bridge/whatsapp-reader-bridge is missing: build it with 'cd bridge && CGO_ENABLED=0 go build' (on any machine) and copy it here" >&2
  exit 1
fi
mkdir -p ~/.config/systemd/user ~/.local/bin
install -m 755 "$here/whatsapp-reader-wrapper.sh" ~/.local/bin/whatsapp-reader
ln -sf "$here/whatsapp-reader-bridge.service" ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable whatsapp-reader-bridge.service
# Keep user services running when you're not logged in.
loginctl enable-linger "$(id -un)"
if [ -f "$here/../data/session.db" ]; then
  systemctl --user restart whatsapp-reader-bridge.service
else
  echo "Not linked yet. Run: uv run whatsapp-reader link   (then the service starts itself)"
fi
