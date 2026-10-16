"""Replace people with stable, irreversible labels before anything leaves the machine.

Labels are HMAC-SHA256(secret, jid_user). Without the secret, the ~1e9 possible UK
mobile numbers can't be brute-forced back from a label. Names typed inside message
text ("ask Priya") are NOT caught; that's a known limitation.
"""

import hashlib
import hmac
import re
import secrets
from pathlib import Path

from .extract import Message

MENTION = re.compile(r"@(\d{6,})")
PHONE = re.compile(r"\+?\d[\d\s\-()]{8,}\d")
EMAIL = re.compile(r"[\w.+-]+@[\w-]+\.[\w.-]+")
# Query strings and fragments often carry signed tokens (presigned S3 links from
# Moodle, share links); host and path are all a summary needs.
URL = re.compile(r"(https?://[^\s?#]+)[?#]\S*")


def load_or_create_secret(path: Path) -> bytes:
    if path.exists():
        return path.read_bytes()
    path.parent.mkdir(parents=True, exist_ok=True)
    secret = secrets.token_bytes(32)
    path.write_bytes(secret)
    path.chmod(0o600)
    return secret


def label(secret: bytes, jid_user: str) -> str:
    digest = hmac.new(secret, jid_user.encode(), hashlib.sha256).hexdigest()
    return f"Member-{digest[:4]}"


def scrub(secret: bytes, text: str) -> str:
    text = URL.sub(r"\1", text)
    text = MENTION.sub(lambda m: "@" + label(secret, m.group(1)), text)
    text = EMAIL.sub("[email]", text)
    # Require 11+ digits so dates and times ("2026-10-08 19:00") survive.
    return PHONE.sub(
        lambda m: "[phone]" if sum(c.isdigit() for c in m.group()) >= 11 else m.group(),
        text,
    )


def transcript(secret: bytes | None, messages: list[Message], names: dict[str, str] | None = None) -> str:
    """Plain-text transcript, one line per message, reactions appended in brackets.

    Default: senders become HMAC labels and text is scrubbed. With `names` (a group
    whose members agreed to pseudonymise = false), senders and @mentions are shown
    by name, falling back to the raw ID, and text is left untouched.
    """
    if names is None:
        assert secret is not None
        who = lambda u: label(secret, u)  # noqa: E731
        clean = lambda t: scrub(secret, t)  # noqa: E731
    else:
        who = lambda u: names.get(u, u)  # noqa: E731
        clean = lambda t: MENTION.sub(lambda m: "@" + names.get(m.group(1), m.group(1)), t)  # noqa: E731

    def person(u: str | None) -> str:
        return "Me" if u is None else who(u)

    lines = []
    for m in messages:
        when = m.sent_at.strftime("%a %d %b %H:%M")
        line = f"[{when}] {person(m.sender)}: {clean(m.text)}"
        if m.reactions:
            line += " [" + ", ".join(f"{e} {person(u)}" for u, e in m.reactions) + "]"
        lines.append(line)
    return "\n".join(lines)
