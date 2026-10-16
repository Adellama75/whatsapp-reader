import argparse
import json
import os
import subprocess
import sys
from datetime import date, datetime, timedelta, timezone
from pathlib import Path

from . import config as cfg
from .decrypt import decrypt
from .extract import group_index, list_groups, messages_since
from .pseudonymise import load_or_create_secret, transcript


def _load_state(path: Path) -> dict[str, str]:
    return json.loads(path.read_text()) if path.exists() else {}


def _client():
    import anthropic

    # User-scoped keys (sk-ant-usr-...) must name a workspace; workspace-scoped keys don't.
    workspace = os.environ.get("ANTHROPIC_WORKSPACE_ID")
    return anthropic.Anthropic(
        default_headers={"anthropic-workspace-id": workspace} if workspace else None
    )


def _find_group(c: cfg.Config, name: str) -> cfg.Group:
    """Exact (case-insensitive) match on label or JID among allowlisted groups."""
    for g in c.groups:
        if name.lower() in (g.label.lower(), g.jid.lower()):
            return g
    labels = ", ".join(g.label for g in c.groups) or "(none)"
    sys.exit(f"'{name}' is not an allowlisted group. Allowlisted: {labels}")


def cmd_fetch(c: cfg.Config, args) -> None:
    """Copy the encrypted backup from the phone (over SSH) into data/."""
    if not c.phone:
        sys.exit("No [paths] phone configured.")
    c.backup.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["scp", "-q", c.phone, str(c.backup)], check=True)
    print(f"fetched {c.backup} ({c.backup.stat().st_size // 1_000_000} MB)")


def cmd_list_groups(c: cfg.Config, args) -> None:
    for jid, subject in list_groups(c.decrypted_db):
        print(f"{jid}\t{subject}")


def cmd_index(c: cfg.Config, args) -> None:
    """Write data/groups.tsv: every group with kind, size, activity and likely community."""
    allow = {g.jid for g in c.groups}
    lines = ["jid\tsubject\tkind\tmembers\tmessages\tlast_message\tcommunity\tshared\tallowlisted"]
    for g in group_index(c.decrypted_db):
        last = g.last_message.strftime("%Y-%m-%d %H:%M") if g.last_message else ""
        parent = f"{g.parent} ({g.parent_jid})" if g.parent else ""
        shared = f"{g.shared}/{g.members}" if g.parent else ""
        lines.append(
            f"{g.jid}\t{g.subject}\t{g.kind}\t{g.members}\t{g.messages}\t{last}\t{parent}\t{shared}\t{'yes' if g.jid in allow else ''}"
        )
    text = "\n".join(lines) + "\n"
    out = c.data_dir / "groups.tsv"
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(text)
    print(text, end="")
    print(f"wrote {out}", file=sys.stderr)


def cmd_groups(c: cfg.Config, args) -> None:
    for g in c.groups:
        print(f"{g.jid}\t{g.label}")


def cmd_run(c: cfg.Config, args) -> None:
    secret = load_or_create_secret(c.secret_file)
    state = _load_state(c.state_file)
    default_since = datetime.now(timezone.utc) - timedelta(days=args.days)
    now = datetime.now(timezone.utc)

    transcripts = {}
    for g in c.groups:
        since = datetime.fromisoformat(state[g.jid]) if g.jid in state else default_since
        msgs = messages_since(c.decrypted_db, g.jid, since)
        if msgs:
            transcripts[g] = transcript(secret, msgs)

    if args.dry_run:
        # Shows exactly what would be sent to the API.
        for g, t in transcripts.items():
            print(f"===== {g.label} =====\n{t}\n")
        return

    from .render import render
    from .summarise import summarise

    client = _client()
    digests = {g.label: summarise(client, c.model, c.effort, g.label, t) for g, t in transcripts.items()}

    if not digests:
        print("Nothing new since the last run.")
    else:
        out = c.data_dir / "digests" / f"{date.today()}.md"
        out.parent.mkdir(parents=True, exist_ok=True)
        text = render(date.today(), digests)
        if out.exists():
            # Second run today: add to the day's digest rather than replacing it.
            text = out.read_text() + f"\n---\n_Update {datetime.now():%H:%M}_\n\n" + text.split("\n", 2)[2]
        out.write_text(text)
        print(out.read_text())

    state.update({g.jid: now.isoformat() for g in c.groups})
    c.state_file.write_text(json.dumps(state, indent=2))


def cmd_summarise(c: cfg.Config, args) -> None:
    """One-off digest of a single allowlisted group; leaves the daily state untouched."""
    g = _find_group(c, args.group)
    since = datetime.now(timezone.utc) - timedelta(days=args.days)
    msgs = messages_since(c.decrypted_db, g.jid, since)
    if not msgs:
        print(f"No messages in {g.label} in the last {args.days} day(s).")
        return
    t = transcript(load_or_create_secret(c.secret_file), msgs)
    if args.dry_run:
        print(t)
        return

    from .render import render
    from .summarise import summarise

    print(render(date.today(), {g.label: summarise(_client(), c.model, c.effort, g.label, t)}))


def cmd_show(c: cfg.Config, args) -> None:
    """Print a stored daily digest (today's by default, else the latest)."""
    digests = c.data_dir / "digests"
    if args.date:
        path = digests / f"{args.date}.md"
    else:
        found = sorted(digests.glob("*.md")) if digests.exists() else []
        if not found:
            sys.exit("No digests yet.")
        path = found[-1]
    if not path.exists():
        sys.exit(f"No digest for {args.date}.")
    print(path.read_text())


def main() -> None:
    p = argparse.ArgumentParser(prog="wareader")
    p.add_argument("--config", type=Path, default=Path("config.toml"))
    p.add_argument("--decrypt", action="store_true", help="decrypt the backup first")
    sub = p.add_subparsers(required=True)

    sub.add_parser("fetch", help="copy the backup from the phone over SSH").set_defaults(fn=cmd_fetch)
    sub.add_parser("list-groups", help="print every group ID and name in the backup").set_defaults(fn=cmd_list_groups)
    sub.add_parser("groups", help="print the allowlisted groups").set_defaults(fn=cmd_groups)
    sub.add_parser("index", help="write data/groups.tsv: all groups with kind, size, activity, likely community").set_defaults(fn=cmd_index)

    run = sub.add_parser("run", help="summarise new messages in allowlisted groups")
    run.add_argument("--days", type=int, default=1, help="lookback on first run")
    run.add_argument("--dry-run", action="store_true", help="print pseudonymised transcripts, don't call the API")
    run.set_defaults(fn=cmd_run)

    s = sub.add_parser("summarise", help="one-off digest of one allowlisted group")
    s.add_argument("group", help="label or JID, as in config.toml")
    s.add_argument("--days", type=int, default=1)
    s.add_argument("--dry-run", action="store_true")
    s.set_defaults(fn=cmd_summarise)

    show = sub.add_parser("show", help="print a stored daily digest")
    show.add_argument("date", nargs="?", help="YYYY-MM-DD (default: latest)")
    show.set_defaults(fn=cmd_show)

    args = p.parse_args()
    c = cfg.load(args.config)
    if args.decrypt:
        decrypt(c.backup, c.key_file, c.decrypted_db)
    args.fn(c, args)
