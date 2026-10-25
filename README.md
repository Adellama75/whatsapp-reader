# whatsapp-reader

Live, pseudonymised transcripts and on-demand digests of the WhatsApp groups you've joined (events, deadlines and announcements), read through a **linked device** that you run yourself. Messages are available the moment they arrive; nothing is scheduled.

## How it works

```
phone ─(WhatsApp multi-device)─▶ bridge ─▶ data/live.db ─▶ whatsapp-reader transcript / summarise / follow
                                                              └─pseudonymise─▶ Claude ─▶ digest
```

1. **Bridge:** `bridge/whatsapp-reader-bridge` is a small Go daemon built on [whatsmeow](https://github.com/tulir/whatsmeow). It pairs with your phone exactly like WhatsApp Web (QR code), stays connected, and writes every message from an **allowlisted group** to `data/live.db`. On first pairing the phone sends a backlog (history sync), so you start with recent history rather than an empty archive. Direct messages and non-allowlisted groups are never stored.
2. **Pseudonymise:** senders become `Member-xxxx` (HMAC-SHA256 with a local secret); phone numbers, emails and @mentions are scrubbed. Reactions and quoted replies stay attached to the message they answer.
3. **Read:** `whatsapp-reader transcript` prints the archive without touching any API. `whatsapp-reader summarise` sends the pseudonymised text to Claude and prints structured events, deadlines and announcements. `whatsapp-reader follow` streams new messages live.

The bridge only listens: it never posts and never marks anything read. Media is not downloaded (an image becomes `[image] caption`) unless a group that has agreed sets `save_media = true`. It is an unofficial client, so it sits outside WhatsApp's terms of service; use it on an account you are prepared to lose.

## Setup

```sh
uv sync
cp config.example.toml config.toml
(cd bridge && CGO_ENABLED=0 go build -ldflags='-s -w' -o whatsapp-reader-bridge .)   # Go 1.27+; static binary, copy it anywhere
uv run whatsapp-reader link              # shows a QR code: WhatsApp > Linked devices > Link a device
./bridge/whatsapp-reader-bridge          # or install it as a service, below
uv run whatsapp-reader index             # every joined group: kind, size, community; add consenting ones to config.toml
uv run whatsapp-reader transcript "Course chat" --days 1
```

For digests set `ANTHROPIC_API_KEY` (a user-scoped key `sk-ant-usr-…` also needs `ANTHROPIC_WORKSPACE_ID`), then `uv run whatsapp-reader summarise "Course chat" --days 7`.

## Running it as a service

The bridge is a systemd user service, so it needs no root. On the machine that will stay online (a home server works well):

```sh
git clone https://github.com/Adellama75/whatsapp-reader ~/dev/whatsapp-reader && cd ~/dev/whatsapp-reader
uv sync && cp config.example.toml config.toml
mkdir -p ~/.config/whatsapp-reader && chmod 700 ~/.config/whatsapp-reader   # pseudonym.secret lives here; copy it from your laptop so labels match
# copy bridge/whatsapp-reader-bridge here (built anywhere with Go)
./deploy/install.sh
uv run whatsapp-reader link              # pair, then the service starts itself
uv run whatsapp-reader status            # "bridge: connected since …" plus freshness per group
```

Read it from another machine with `ssh server 'cd dev/whatsapp-reader && uv run whatsapp-reader transcript "Course chat"'`. Nothing is pushed anywhere.

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
| `agent [--once \| --dry-run]` | new messages in the `[agent]` group → Claude Code → GitHub issues |

`<group>` is the label or JID from `config.toml`, matched case-insensitively.

## Finding the right group

Community sub-groups are usually all called "General". `whatsapp-reader index` lists every joined group with its `kind` (group, community, announcements, subgroup) and, for sub-groups, the community they belong to, which the protocol reports directly.

Adding a group to `config.toml` takes effect within seconds: the bridge re-reads the allowlist when the file changes and starts archiving that group's new messages. Messages from before it was allowlisted are not stored (the backlog from pairing is filtered by the allowlist at the time it arrives).

## The issue-tracking agent

`whatsapp-reader agent` watches one group that works out of a GitHub repo. Each burst of new messages (batched with a
quiet-period debounce, or at once when someone addresses it by name or replies to one of its notes) becomes one
headless `claude -p` run with `gh issue` as its only write tool, driven by [chat-agent](https://github.com/Adellama75/chat-agent):
open, comment on or close issues for tasks, blockers and decisions the team states; otherwise do nothing. Its
output is the issues; it never writes to the group. `--dry-run` prints exactly what Claude would see;
`data/agent.log` records every run; `touch data/agent.paused` holds runs without stopping the service.

```sh
gh auth login && git clone https://github.com/owner/name ~/dev/name   # claude must also be logged in
# add [agent] to config.toml (see config.example.toml)
uv run whatsapp-reader agent --dry-run   # look at the prompt
./deploy/install-agent.sh                # systemd user service, next to the bridge
```

`whatsapp-reader follow` streams each burst (with message ids) to an interactive Claude session and keeps
`data/coordinator.alive` fresh while it reads the agent's group, so you can sit in on the chat from a terminal;
it resumes where the last `follow` stopped. The agent itself (batching, prompt, chat style, coordinator) is
chat-agent, shared with [discord-reader](https://github.com/Adellama75/discord-reader); clone chat-agent next to this
repo (`../chat-agent`) before `uv sync`. This repo only has the WhatsApp adapter (`src/whatsapp_reader/chat.py`).

Plugins installed next to whatsapp-reader (entry-point group `whatsapp_reader.plugins`, see `src/whatsapp_reader/plugins.py`) can add
commands and agent tools; bridge additions go in `bridge/local_*.go`. None ship with this repo.

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
