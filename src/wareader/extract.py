"""Read group messages out of a decrypted Android msgstore.db.

The schema is undocumented and shifts between WhatsApp versions. These queries
target the post-2022 layout (`message`, `chat`, `jid` tables); check them against
your own DB with `sqlite3 data/msgstore.db .schema` if anything comes back empty.
"""

import sqlite3
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path


@dataclass
class Message:
    chat_jid: str
    sent_at: datetime
    sender: str | None  # jid user part (phone number or LID); None means you sent it
    text: str


def list_groups(db: Path) -> list[tuple[str, str]]:
    """(jid, subject) for every group chat."""
    with sqlite3.connect(f"file:{db}?mode=ro", uri=True) as conn:
        return conn.execute(
            """
            SELECT jid.raw_string, chat.subject
            FROM chat JOIN jid ON jid._id = chat.jid_row_id
            WHERE jid.server = 'g.us'
            ORDER BY chat.subject
            """
        ).fetchall()


def messages_since(db: Path, chat_jid: str, since: datetime) -> list[Message]:
    """Text messages in one group newer than `since`, oldest first."""
    since_ms = int(since.timestamp() * 1000)
    with sqlite3.connect(f"file:{db}?mode=ro", uri=True) as conn:
        rows = conn.execute(
            """
            SELECT m.timestamp, m.from_me, sender.user, m.text_data
            FROM message m
            JOIN chat ON chat._id = m.chat_row_id
            JOIN jid AS chat_jid ON chat_jid._id = chat.jid_row_id
            LEFT JOIN jid AS sender ON sender._id = m.sender_jid_row_id
            WHERE chat_jid.raw_string = ?
              AND m.timestamp > ?
              AND m.text_data IS NOT NULL AND m.text_data != ''
            ORDER BY m.timestamp
            """,
            (chat_jid, since_ms),
        ).fetchall()
    return [
        Message(
            chat_jid=chat_jid,
            sent_at=datetime.fromtimestamp(ts / 1000, tz=timezone.utc),
            sender=None if from_me else user,
            text=text,
        )
        for ts, from_me, user, text in rows
    ]
