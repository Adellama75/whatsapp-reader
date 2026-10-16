# wareader

Live, pseudonymised transcripts and on-demand digests of the WhatsApp groups you've joined (events, deadlines and announcements), read through a **linked device** that you run yourself. Messages are available the moment they arrive; nothing is scheduled.

## How it works

```
phone ─(WhatsApp multi-device)─▶ bridge ─▶ data/live.db ─▶ wareader transcript / summarise / follow
                                                              └─pseudonymise─▶ Claude ─▶ digest
```

1. **Bridge:** `bridge/wareader-bridge` is a small Go daemon built on [whatsmeow](https://github.com/tulir/whatsmeow). It pairs with your phone exactly like WhatsApp Web (QR code), stays connected, and writes every message from an **allowlisted group** to `data/live.db`. On first pairing the phone sends a backlog (history sync), so you start with recent history rather than an empty archive. Direct messages and non-allowlisted groups are never stored.
2. **Pseudonymise:** senders become `Member-xxxx` (HMAC-SHA256 with a local secret); phone numbers, emails and @mentions are scrubbed. Reactions and quoted replies stay attached to the message they answer.
3. **Read:** `wareader transcript` prints the archive without touching any API. `wareader summarise` sends the pseudonymised text to Claude and prints structured events, deadlines and announcements. `wareader follow` streams new messages live.

The bridge only listens: it never posts, never marks anything read, and downloads no media (an image becomes `[image] caption`). It is an unofficial client, so it sits outside WhatsApp's terms of service; use it on an account you are prepared to lose.

## Setup

```sh
uv sync
cp config.example.toml config.toml
(cd bridge && CGO_ENABLED=0 go build -ldflags='-s -w' -o wareader-bridge .)   # Go 1.27+; static binary, copy it anywhere
uv run wareader link              # shows a QR code: WhatsApp > Linked devices > Link a device
./bridge/wareader-bridge          # or install it as a service, below
uv run wareader index             # every joined group: kind, size, community; add consenting ones to config.toml
uv run wareader transcript "Course chat" --days 1
```

For digests set `ANTHROPIC_API_KEY` (a user-scoped key `sk-ant-usr-…` also needs `ANTHROPIC_WORKSPACE_ID`), then `uv run wareader summarise "Course chat" --days 7`.

## Running it as a service

The bridge is a systemd user service, so it needs no root. On the machine that will stay online (a home server works well):

```sh
git clone https://github.com/Adellama75/wareader ~/dev/wareader && cd ~/dev/wareader
uv sync && cp config.example.toml config.toml
mkdir -p ~/.config/wareader && chmod 700 ~/.config/wareader   # pseudonym.secret lives here; copy it from your laptop so labels match
# copy bridge/wareader-bridge here (built anywhere with Go)
./deploy/install.sh
uv run wareader link              # pair, then the service starts itself
uv run wareader status            # "bridge: connected since …" plus freshness per group
```

Read it from another machine with `ssh server 'cd dev/wareader && uv run wareader transcript "Course chat"'`. Nothing is pushed anywhere.

## Commands

| | |
|---|---|
| `transcript <group> [--days N \| --since T]` | pseudonymised messages, no API call |
| `summarise <group> [--days N \| --since T] [--dry-run]` | digest via Claude; `--dry-run` shows exactly what would be sent |
| `follow [group]` | stream new messages as they arrive (Ctrl-C to stop) |
| `status` | bridge state, message count and last message per allowlisted group |
| `index` | `data/groups.tsv`: every joined group with kind, members, community, allowlisted |
| `groups` | the allowlist |
| `link` | pair with the phone (stops/starts the service around it) |

`<group>` is the label or JID from `config.toml`, matched case-insensitively.

## Finding the right group

Community sub-groups are usually all called "General". `wareader index` lists every joined group with its `kind` (group, community, announcements, subgroup) and, for sub-groups, the community they belong to, which the protocol reports directly.

Adding a group to `config.toml` takes effect within seconds: the bridge re-reads the allowlist when the file changes and starts archiving that group's new messages. Messages from before it was allowlisted are not stored (the backlog from pairing is filtered by the allowlist at the time it arrives).

## Groups that have agreed to real names

Pseudonymisation is per group. For a small working group where everyone has agreed, set `pseudonymise = false` on its entry; transcripts and digests then show senders by the name they set on their own WhatsApp profile, and leave text unscrubbed. A `[names]` table overrides profile names, keyed by the sender ID shown in the raw transcript:

```toml
[[groups]]
jid = "1203630000000000@g.us"
label = "Project team"
pseudonymise = false

[names]
"57153196953645" = "Alice"
```

Note that a digest for such a group is sent to the Claude API with names intact.

## Privacy

- Only allowlisted groups are stored. Ask a group before adding it, and only turn off pseudonymisation for a group that has explicitly agreed.
- Phone numbers never leave your machine; labels can't be reversed without the local secret. Senders are keyed by WhatsApp's per-account LID where available, so one person keeps one label whether they appear by LID or by number.
- "Delete for everyone" is honoured: a revoked message and its reactions are removed from the archive.
- **Known limitation:** names typed in message text ("ask Priya") are not removed.
- `data/session.db` holds the device keys; anyone with it can read your chats until you unlink the device in WhatsApp. The `.gitignore` excludes it, the archive, the secret and `config.toml`. Tests use synthetic data only.

## License

[AGPL-3.0-or-later](LICENSE). If you share a modified version, or run one as a service for others, you must publish its source, so any privacy protections stay inspectable.
