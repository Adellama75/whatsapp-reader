import os
import re
import shutil
import subprocess
import sys
import time
from datetime import date, datetime, timedelta, timezone
from pathlib import Path

from . import config as cfg
from .pseudonymise import MENTION, load_or_create_secret, transcript
from .store import (
    Message,
    attachment,
    contacts,
    group_index,
    last_rowid,
    messages_after,
    messages_by_id,
    messages_since,
    own_ids,
    poll_votes,
    push_names,
    status,
)


def _client():
    import anthropic

    # User-scoped keys (sk-ant-usr-...) must name a workspace; workspace-scoped keys don't.
    workspace = os.environ.get("ANTHROPIC_WORKSPACE_ID")
    return anthropic.Anthropic(
        default_headers={"anthropic-workspace-id": workspace} if workspace else None
    )


def _transcript(c: cfg.Config, g: cfg.Group, msgs: list[Message], to_claude=None, ids: bool = False) -> str:
    me = own_ids(c.live_db)
    if g.pseudonymise:
        return transcript(load_or_create_secret(c.secret_file), msgs, me=me, to_claude=to_claude, ids=ids)
    users = {m.sender for m in msgs if m.sender} | {u for m in msgs for u, _ in m.reactions if u}
    users |= {u for m in msgs for t in (m.text, m.quoted or "") for u in MENTION.findall(t)}  # @-mentioned, maybe silent
    users |= {u for m in msgs if m.poll for _, voters in m.poll[1] for u in voters if u}
    names = push_names(c.live_db, users) | c.names  # config.toml [names] wins over profile names
    return transcript(None, msgs, names=names, me=me, to_claude=to_claude, ids=ids)


def _find_group(c: cfg.Config, name: str) -> cfg.Group:
    """Exact (case-insensitive) match on label or JID among allowlisted groups."""
    for g in c.groups:
        if name.lower() in (g.label.lower(), g.jid.lower()):
            return g
    labels = ", ".join(g.label for g in c.groups) or "(none)"
    sys.exit(f"'{name}' is not an allowlisted group. Allowlisted: {labels}")


def _since(args) -> datetime:
    if args.since:
        dt = datetime.fromisoformat(args.since)
        return dt if dt.tzinfo else dt.astimezone()
    return datetime.now(timezone.utc) - timedelta(days=args.days)


def cmd_groups(c: cfg.Config, args) -> None:
    for g in c.groups:
        print(f"{g.jid}\t{g.label}")


def cmd_index(c: cfg.Config, args) -> None:
    """Write data/groups.tsv: every joined group with kind, size, activity and community."""
    allow = {g.jid for g in c.groups}
    lines = ["jid\tsubject\tkind\tmembers\tmessages\tlast_message\tcommunity\tallowlisted"]
    for g in group_index(c.live_db):
        last = g.last_message.strftime("%Y-%m-%d %H:%M") if g.last_message else ""
        parent = f"{g.parent} ({g.parent_jid})" if g.parent_jid else ""
        lines.append(
            f"{g.jid}\t{g.subject}\t{g.kind}\t{g.members}\t{g.messages}\t{last}\t{parent}\t{'yes' if g.jid in allow else ''}"
        )
    text = "\n".join(lines) + "\n"
    out = c.data_dir / "groups.tsv"
    out.write_text(text)
    print(text, end="")
    print(f"wrote {out}", file=sys.stderr)


def cmd_contacts(c: cfg.Config, args) -> None:
    """Find a person's direct-chat JID by name, to list that chat in config.toml as a [[groups]] entry."""
    allow = {g.jid for g in c.groups}
    found = contacts(c.data_dir / "session.db", c.live_db, args.name)
    if not found:
        print(f"No contact matching {args.name!r}.")
    for p in found:
        listed = "allowlisted" if {p.jid, f"{p.lid}@lid", f"{p.pn}@s.whatsapp.net"} & allow else ""
        number = f"\t{p.pn}@s.whatsapp.net" if args.numbers and p.pn and p.lid else ""
        print(f"{p.jid}\t{p.name}{number}\t{listed}".rstrip("\t"))


def cmd_transcript(c: cfg.Config, args) -> None:
    """Print the pseudonymised messages of one allowlisted group; never calls the API."""
    g = _find_group(c, args.group)
    msgs = messages_since(c.live_db, g.jid, _since(args))
    if not msgs:
        print(f"No messages in {g.label} since {_since(args):%Y-%m-%d %H:%M}.")
        return
    print(_transcript(c, g, msgs, ids=args.ids))


def cmd_summarise(c: cfg.Config, args) -> None:
    """Digest of one allowlisted group over a period, straight from the live archive."""
    g = _find_group(c, args.group)
    msgs = messages_since(c.live_db, g.jid, _since(args))
    if not msgs:
        print(f"No messages in {g.label} since {_since(args):%Y-%m-%d %H:%M}.")
        return
    t = _transcript(c, g, msgs)
    if args.dry_run:
        print(t)
        return

    from .render import render
    from .summarise import summarise

    print(render(date.today(), {g.label: summarise(_client(), c.model, c.effort, g.label, t)}))


def cmd_follow(c: cfg.Config, args) -> None:
    """Stream bursts of new messages, with ids, for a coordinator session (like `tail -f`); see
    chatagent.follow. Following the [agent] group keeps data/coordinator.alive fresh, so the agent
    service leaves replies to whoever reads this."""
    from chatagent import follow

    from .chat import WhatsAppChat

    prefix = c.agent.prefix if c.agent else ""
    chats = [WhatsAppChat(c, g, prefix, _transcript) for g in ([_find_group(c, args.group)] if args.group else c.groups)]
    agent_chat = next((x for x in chats if c.agent and c.agent.group.lower() in (x.g.label.lower(), x.g.jid.lower())), None)
    follow(chats, c.agent, agent_chat=agent_chat, after=args.after, interval=args.interval, polls=args.polls,
           prefix_labels=not args.group)


def cmd_tail(c: cfg.Config, args) -> None:
    """Print each new message of one group as soon as it is stored, as transcript lines with ids, and
    a `[poll update]` line with the new tally whenever someone votes on a poll.
    Unlike follow: no batching or debounce, and no data/follow.cursor (pass --after to resume)."""
    from .chat import replies_to_bot

    g = _find_group(c, args.group)
    prefix = c.agent.prefix if c.agent else ""
    to_claude = lambda m: replies_to_bot(m, prefix)  # noqa: E731
    cursor = last_rowid(c.live_db) if args.after is None else args.after
    votes = poll_votes(c.live_db, g.jid)
    n = 0
    while args.polls is None or n < args.polls:
        n += 1
        msgs = messages_after(c.live_db, g.jid, cursor)
        if msgs:
            for line in _transcript(c, g, msgs, to_claude=to_claude, ids=True).splitlines():
                print(line, flush=True)
            cursor = max(m.rowid for m in msgs)
        now = poll_votes(c.live_db, g.jid)
        printed = {m.id for m in msgs}  # a poll that just arrived already shows its tally
        changed = sorted({k[0] for k in now.keys() | votes.keys() if now.get(k) != votes.get(k)} - printed)
        votes = now
        polls = messages_by_id(c.live_db, g.jid, changed)
        if polls:
            for line in _transcript(c, g, polls, to_claude=to_claude, ids=True).splitlines():
                print(f"[poll update] {line}", flush=True)
        time.sleep(args.interval)


def cmd_agent(c: cfg.Config, args) -> None:
    """Watch the [agent] group and run Claude Code on each burst of new messages (chat-agent)."""
    from chatagent import AgentLoop

    from .chat import WhatsAppChat

    if c.agent is None:
        sys.exit("no [agent] section in config.toml")
    g = next((g for g in c.groups if c.agent.group.lower() in (g.label.lower(), g.jid.lower())), None)
    if g is None:
        sys.exit(f"[agent] group '{c.agent.group}' is not allowlisted")
    loop = AgentLoop(c.agent, WhatsAppChat(c, g, c.agent.prefix, _transcript))
    if args.once or args.dry_run:
        out = loop.once(dry_run=args.dry_run)
        if out:
            print(out)
        return
    loop.serve(interval=args.interval, polls=args.polls)


MEDIA_KINDS = ("image", "document", "video", "audio")


def cmd_download(c: cfg.Config, args) -> None:
    """Save the attachment of one message (an id from `transcript --ids`): the bridge downloads it on
    request into data/attachments, asking the phone for the keys of messages it stored without them."""
    g = _find_group(c, args.group)
    if not re.fullmatch(r"[0-9A-Za-z]+", args.id):
        sys.exit(f"'{args.id}' is not a message id")
    found = attachment(c.live_db, g.jid, args.id)
    if found is None:
        sys.exit(f"no message {args.id} in {g.label}")
    kind, file = found
    if kind not in MEDIA_KINDS:
        sys.exit(f"message {args.id} is a {kind}, not an attachment")
    if not (file and Path(file).exists()):
        requests = c.data_dir / "requests"
        requests.mkdir(mode=0o700, exist_ok=True)
        want, err = requests / f"{args.id}.want", requests / f"{args.id}.err"
        err.unlink(missing_ok=True)
        want.write_text(g.jid + "\n")
        deadline = time.monotonic() + args.timeout
        while not (file and Path(file).exists()):
            if err.exists():
                sys.exit(f"download failed: {err.read_text().strip()}")
            if time.monotonic() > deadline:
                want.unlink(missing_ok=True)
                sys.exit("timed out waiting for the bridge (is it running? see `whatsapp-reader status`)")
            time.sleep(args.interval)
            _, file = attachment(c.live_db, g.jid, args.id)
    if args.output:
        shutil.copyfile(file, args.output)
        file = str(args.output)
    print(file)


def cmd_status(c: cfg.Config, args) -> None:
    """Is the bridge connected, and how fresh is each allowlisted group?"""
    state, per_group = status(c.live_db, [g.jid for g in c.groups])
    since = datetime.fromtimestamp(int(state["since"]), tz=timezone.utc).astimezone() if state.get("since") else None
    print(f"bridge: {state.get('state', 'unknown')}" + (f" since {since:%Y-%m-%d %H:%M}" if since else ""))
    for g in c.groups:
        n, last = per_group[g.jid]
        when = last.astimezone().strftime("%Y-%m-%d %H:%M") if last else "never"
        print(f"{g.label}: {n} messages, last {when}")


def cmd_link(c: cfg.Config, args) -> None:
    """Pair this machine as a linked device: shows a QR code to scan with the phone."""
    unit = "whatsapp-reader-bridge"
    running = subprocess.run(["systemctl", "--user", "is-active", "--quiet", unit]).returncode == 0
    if running:
        subprocess.run(["systemctl", "--user", "stop", unit], check=True)
    cmd = [str(c.bridge), "-pair", "-data", str(c.data_dir), "-config", str(args.config), "-qr", args.qr]
    if args.phone:
        cmd += ["-phone", args.phone]
    subprocess.run(cmd, check=True)
    if running or subprocess.run(["systemctl", "--user", "is-enabled", "--quiet", unit]).returncode == 0:
        subprocess.run(["systemctl", "--user", "start", unit], check=True)
        print("bridge started")


def main() -> None:
    import argparse

    p = argparse.ArgumentParser(prog="whatsapp-reader")
    # WHATSAPP_READER_CONFIG lets subprocesses (tools Claude runs under `whatsapp-reader agent`) find the same config
    # from any working directory.
    p.add_argument("--config", type=Path, default=Path(os.environ.get("WHATSAPP_READER_CONFIG", "config.toml")))
    sub = p.add_subparsers(required=True)

    sub.add_parser("groups", help="print the allowlisted groups").set_defaults(fn=cmd_groups)
    sub.add_parser("index", help="write data/groups.tsv: all joined groups with kind, size, activity, community").set_defaults(fn=cmd_index)
    sub.add_parser("status", help="bridge connection state and per-group freshness").set_defaults(fn=cmd_status)
    link = sub.add_parser("link", help="pair with the phone (QR code or link code), then (re)start the bridge")
    link.add_argument("--phone", help="your number in international format (e.g. 447700900123): get an 8-character code instead of a QR")
    link.add_argument("--qr", choices=["half", "full"], default="half", help="QR size; 'full' if the phone won't scan 'half'")
    link.set_defaults(fn=cmd_link)

    def period(sp):
        sp.add_argument("--days", type=float, default=1, help="lookback (default 1)")
        sp.add_argument("--since", help="ISO date/time instead of --days, e.g. 2026-10-16T09:00")

    ct = sub.add_parser("contacts", help="find a person's direct-chat JID by name (to allowlist that chat)")
    ct.add_argument("name", help="part of the name saved on the phone, or of their profile name")
    ct.add_argument("--numbers", action="store_true", help="also show the phone-number JID")
    ct.set_defaults(fn=cmd_contacts)

    t = sub.add_parser("transcript", help="print the raw pseudonymised messages of one allowlisted group")
    t.add_argument("group", help="label or JID, as in config.toml")
    period(t)
    t.add_argument("--ids", action="store_true", help="show message ids (#ID)")
    t.set_defaults(fn=cmd_transcript)

    s = sub.add_parser("summarise", help="digest of one allowlisted group (calls the Claude API)")
    s.add_argument("group", help="label or JID, as in config.toml")
    period(s)
    s.add_argument("--dry-run", action="store_true", help="print the transcript that would be sent instead")
    s.set_defaults(fn=cmd_summarise)

    f = sub.add_parser("follow", help="stream new messages live (all allowlisted groups, or one)")
    f.add_argument("group", nargs="?", help="label or JID; default: every allowlisted group")
    f.add_argument("--after", type=int, help="start after this archive rowid (default: where the last follow stopped)")
    f.add_argument("--interval", type=float, default=2, help="seconds between polls")
    f.add_argument("--polls", type=int, help="stop after this many polls (default: run until Ctrl-C)")
    f.set_defaults(fn=cmd_follow)

    tl = sub.add_parser("tail", help="stream one group's new messages as transcript lines, no batching or cursor file")
    tl.add_argument("group", help="label or JID, as in config.toml")
    tl.add_argument("--after", type=int, help="start after this archive rowid (default: only messages from now on)")
    tl.add_argument("--interval", type=float, default=2, help="seconds between polls")
    tl.add_argument("--polls", type=int, help="stop after this many polls (default: run until Ctrl-C)")
    tl.set_defaults(fn=cmd_tail)

    d = sub.add_parser("download", help="save the attachment (image, document, video, voice note) of one message")
    d.add_argument("group", help="label or JID, as in config.toml")
    d.add_argument("id", help="message id, as shown by `transcript --ids`")
    d.add_argument("-o", "--output", type=Path, help="copy it here (default: just print where data/attachments has it)")
    d.add_argument("--timeout", type=float, default=120, help="seconds to wait for the bridge (default 120)")
    d.add_argument("--interval", type=float, default=1, help=argparse.SUPPRESS)
    d.set_defaults(fn=cmd_download)

    a = sub.add_parser("agent", help="new messages in the [agent] group -> Claude Code -> GitHub issues + replies")
    a.add_argument("--once", action="store_true", help="process what is pending now, then exit")
    a.add_argument("--dry-run", action="store_true", help="print the prompts Claude would get, change nothing")
    a.add_argument("--interval", type=float, default=2, help="seconds between polls")
    a.add_argument("--polls", type=int, help="stop after this many polls (default: run until Ctrl-C)")
    a.set_defaults(fn=cmd_agent)

    from . import plugins

    for plugin in plugins.load():
        if hasattr(plugin, "register"):
            plugin.register(sub)

    args = p.parse_args()
    os.environ["WHATSAPP_READER_CONFIG"] = str(args.config.resolve())
    c = cfg.load(args.config)
    try:
        args.fn(c, args)
    except FileNotFoundError as e:
        sys.exit(str(e))
    except KeyboardInterrupt:
        pass
