#!/bin/sh
# ~/.local/bin/whatsapp-reader: load the env file, run the CLI from its checkout, from any directory (staying in it, so relative paths work).
# Plugins: list extra packages to load in WHATSAPP_READER_WITH (space-separated paths), e.g. in the env file.
set -a; [ -f "$HOME/.config/whatsapp-reader/env" ] && . "$HOME/.config/whatsapp-reader/env"; set +a
: "${WHATSAPP_READER_CONFIG:=$HOME/dev/whatsapp-reader/config.toml}"; export WHATSAPP_READER_CONFIG
with=""; for p in ${WHATSAPP_READER_WITH:-}; do with="$with --with-editable $p"; done
exec "$HOME/.local/bin/uv" run -q --project "$HOME/dev/whatsapp-reader" $with whatsapp-reader "$@"
