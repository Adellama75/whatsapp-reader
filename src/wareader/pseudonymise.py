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
    text = MENTION.sub(lambda m: "@" + label(secret, m.group(1)), text)
    text = EMAIL.sub("[email]", text)
    # Require 11+ digits so dates and times ("2026-10-08 19:00") survive.
    return PHONE.sub(
        lambda m: "[phone]" if sum(c.isdigit() for c in m.group()) >= 11 else m.group(),
        text,
    )


def transcript(secret: bytes, messages: list[Message]) -> str:
    """Pseudonymised plain-text transcript, one line per message."""
    lines = []
    for m in messages:
        who = "Me" if m.sender is None else label(secret, m.sender)
        when = m.sent_at.strftime("%a %d %b %H:%M")
        lines.append(f"[{when}] {who}: {scrub(secret, m.text)}")
    return "\n".join(lines)
