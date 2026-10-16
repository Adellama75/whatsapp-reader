# wareader

Daily, pseudonymised digests of the WhatsApp groups you've joined (events, deadlines and announcements) built from **your own Android chat backup**. It doesn't use an unofficial client or log in to WhatsApp, and it never talks to WhatsApp's servers.

## How it works

```
msgstore.db.crypt15 ─decrypt─▶ msgstore.db ─extract─▶ messages ─pseudonymise─▶ Claude ─▶ digest.md
```

1. **Decrypt:** WhatsApp writes `msgstore.db.crypt15` every night. With a 64-digit end-to-end backup key, [wa-crypt-tools](https://github.com/ElDavoo/wa-crypt-tools) decrypts it to SQLite.
2. **Extract:** only groups listed in `config.toml` are read.
3. **Pseudonymise:** senders become `Member-xxxx` (HMAC-SHA256 with a local secret), and phone numbers, emails and @mentions are scrubbed.
4. **Summarise:** pseudonymised text goes to the Claude API, which returns structured events, deadlines and announcements.
5. **Render:** a markdown digest is written to `data/digests/`.

## Setup

1. In WhatsApp: Settings → Chats → Chat backup → End-to-end encrypted backup → **use 64-digit key**. Save the key to the `key_file` path (it unlocks *all* your chats, so guard it).
2. Copy `Android/media/com.whatsapp/WhatsApp/Databases/msgstore.db.crypt15` from the phone to `data/`.
3. `cp config.example.toml config.toml`, then set `ANTHROPIC_API_KEY`.
4. Run:

```sh
uv sync
uv run wareader --decrypt list-groups      # find group IDs, add the consenting ones to config.toml
uv run wareader run --dry-run --days 7     # inspect exactly what would be sent
uv run wareader run --days 7
```

## Privacy

- Only allowlisted groups are read. Ask a group before adding it.
- Phone numbers never leave your machine; labels can't be reversed without the local secret.
- **Known limitation:** names typed in message text ("ask Priya") are not removed.
- The `.gitignore` excludes backups, keys, decrypted DBs and digests. Tests use synthetic data only.

## License

[AGPL-3.0-or-later](LICENSE). If you share a modified version, or run one as a service for others, you must publish its source, so any privacy protections stay inspectable.
