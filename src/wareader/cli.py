import argparse
import json
from datetime import date, datetime, timedelta, timezone
from pathlib import Path

from . import config as cfg
from .decrypt import decrypt
from .extract import list_groups, messages_since
from .pseudonymise import load_or_create_secret, transcript


def _load_state(path: Path) -> dict[str, str]:
    return json.loads(path.read_text()) if path.exists() else {}


def cmd_list_groups(c: cfg.Config, args) -> None:
    for jid, subject in list_groups(c.decrypted_db):
        print(f"{jid}\t{subject}")


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

    import anthropic

    from .render import render
    from .summarise import summarise

    client = anthropic.Anthropic()
    digests = {g.label: summarise(client, c.model, c.effort, g.label, t) for g, t in transcripts.items()}

    out = c.data_dir / "digests" / f"{date.today()}.md"
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(render(date.today(), digests))
    print(out.read_text())

    state.update({g.jid: now.isoformat() for g in c.groups})
    c.state_file.write_text(json.dumps(state, indent=2))


def main() -> None:
    p = argparse.ArgumentParser(prog="wareader")
    p.add_argument("--config", type=Path, default=Path("config.toml"))
    p.add_argument("--decrypt", action="store_true", help="decrypt the backup first")
    sub = p.add_subparsers(required=True)

    sub.add_parser("list-groups", help="print group IDs and names").set_defaults(fn=cmd_list_groups)

    run = sub.add_parser("run", help="summarise new messages in allowlisted groups")
    run.add_argument("--days", type=int, default=1, help="lookback on first run")
    run.add_argument("--dry-run", action="store_true", help="print pseudonymised transcripts, don't call the API")
    run.set_defaults(fn=cmd_run)

    args = p.parse_args()
    c = cfg.load(args.config)
    if args.decrypt:
        decrypt(c.backup, c.key_file, c.decrypted_db)
    args.fn(c, args)
