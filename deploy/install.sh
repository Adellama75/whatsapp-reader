#!/bin/sh
# Install the daily timer as a systemd *user* unit (no root needed).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p ~/.config/systemd/user
ln -sf "$here/wareader.service" "$here/wareader.timer" ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now wareader.timer
# Keep user services running when you're not logged in.
loginctl enable-linger "$(id -un)"
systemctl --user list-timers wareader.timer --no-pager
