import os
import subprocess
import sys
import time
from datetime import date, datetime, timedelta, timezone
from pathlib import Path

from . import config as cfg
from .pseudonymise import load_or_create_secret, transcript
from .store import (
    Message,
    group_index,
    last_rowid,
    messages_after,
    messages_since,
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


def _transcript(c: cfg.Config, g: cfg.Group, msgs: list[Message]) -> str:
    if g.pseudonymise:
        return transcript(load_or_create_secret(c.secret_file), msgs)
    users = {m.sender for m in msgs if m.sender} | {u for m in msgs for u, _ in m.reactions if u}
    names = push_names(c.live_db, users) | c.names  # config.toml [names] wins over profile names
    return transcript(None, msgs, names=names)


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


def cmd_transcript(c: cfg.Config, args) -> None:
    """Print the pseudonymised messages of one allowlisted group; never calls the API."""
    g = _find_group(c, args.group)
    msgs = messages_since(c.live_db, g.jid, _since(args))
    if not msgs:
        print(f"No messages in {g.label} since {_since(args):%Y-%m-%d %H:%M}.")
        return
    print(_transcript(c, g, msgs))


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
    """Stream new messages as the bridge stores them (like `tail -f`)."""
    groups = [_find_group(c, args.group)] if args.group else c.groups
    cursor = last_rowid(c.live_db)
    polls = 0
    while args.polls is None or polls < args.polls:
        polls += 1
        for g in groups:
            new = messages_after(c.live_db, g.jid, cursor)
            if new:
                cursor = max(cursor, new[-1].rowid)
                prefix = "" if args.group else f"[{g.label}] "
                for line in _transcript(c, g, new).splitlines():
                    print(prefix + line, flush=True)
        time.sleep(args.interval)


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
    unit = "wareader-bridge"
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

    p = argparse.ArgumentParser(prog="wareader")
    p.add_argument("--config", type=Path, default=Path("config.toml"))
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

    t = sub.add_parser("transcript", help="print the raw pseudonymised messages of one allowlisted group")
    t.add_argument("group", help="label or JID, as in config.toml")
    period(t)
    t.set_defaults(fn=cmd_transcript)

    s = sub.add_parser("summarise", help="digest of one allowlisted group (calls the Claude API)")
    s.add_argument("group", help="label or JID, as in config.toml")
    period(s)
    s.add_argument("--dry-run", action="store_true", help="print the transcript that would be sent instead")
    s.set_defaults(fn=cmd_summarise)

    f = sub.add_parser("follow", help="stream new messages live (all allowlisted groups, or one)")
    f.add_argument("group", nargs="?", help="label or JID; default: every allowlisted group")
    f.add_argument("--interval", type=float, default=2, help="seconds between polls")
    f.add_argument("--polls", type=int, help="stop after this many polls (default: run until Ctrl-C)")
    f.set_defaults(fn=cmd_follow)

    args = p.parse_args()
    c = cfg.load(args.config)
    try:
        args.fn(c, args)
    except FileNotFoundError as e:
        sys.exit(str(e))
    except KeyboardInterrupt:
        pass
