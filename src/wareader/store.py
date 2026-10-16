"""Read-only access to data/live.db, the archive the bridge keeps up to date.

The bridge (bridge/) is the only writer. Every function here opens the file
read-only, so the CLI can never corrupt the archive and can run while the
bridge is writing (SQLite WAL mode allows one writer plus any readers).
"""

import sqlite3
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path


@dataclass
class Message:
    chat_jid: str
    sent_at: datetime
    sender: str | None  # canonical sender ID (usually a LID); None means you sent it
    text: str
    kind: str = "text"
    quoted: str | None = None  # text of the message this one replies to, if stored
    reactions: list[tuple[str | None, str]] = field(default_factory=list)  # (sender, emoji)
    rowid: int = 0


@dataclass
class GroupInfo:
    jid: str
    subject: str
    kind: str  # group, community, announcements, subgroup
    members: int
    parent_jid: str | None
    parent: str | None  # subject of the community this group belongs to
    messages: int  # stored messages (0 unless allowlisted)
    last_message: datetime | None


def _connect(db: Path) -> sqlite3.Connection:
    if not db.exists():
        raise FileNotFoundError(f"{db} does not exist yet: is the bridge running?")
    return sqlite3.connect(f"file:{db}?mode=ro", uri=True)


def _ts(seconds: int | None) -> datetime | None:
    return datetime.fromtimestamp(seconds, tz=timezone.utc) if seconds else None


def _messages(conn: sqlite3.Connection, where: str, params: tuple) -> list[Message]:
    rows = conn.execute(
        f"""
        SELECT m.rowid, m.chat_jid, m.ts, m.from_me, m.sender, m.text, m.kind, q.text
        FROM messages m
        LEFT JOIN messages q ON q.chat_jid = m.chat_jid AND q.id = m.reply_to
        WHERE {where}
        ORDER BY m.ts, m.rowid
        """,
        params,
    ).fetchall()
    if not rows:
        return []
    ids = conn.execute(
        f"SELECT m.id, m.rowid FROM messages m WHERE {where}", params
    ).fetchall()
    by_id = {mid: rid for mid, rid in ids}
    reactions: dict[int, list[tuple[str | None, str]]] = {}
    marks = ",".join("?" * len(by_id))
    for target, sender, emoji in conn.execute(
        f"SELECT target_id, sender, emoji FROM reactions WHERE chat_jid = ? AND target_id IN ({marks}) ORDER BY ts",
        (params[0], *by_id),
    ):
        reactions.setdefault(by_id[target], []).append((sender or None, emoji))
    return [
        Message(
            chat_jid=chat,
            sent_at=_ts(ts),
            sender=None if from_me else sender,
            text=text,
            kind=kind,
            quoted=quoted,
            reactions=reactions.get(rowid, []),
            rowid=rowid,
        )
        for rowid, chat, ts, from_me, sender, text, kind, quoted in rows
    ]


def messages_since(db: Path, chat_jid: str, since: datetime) -> list[Message]:
    """Messages in one group newer than `since`, oldest first."""
    with _connect(db) as conn:
        return _messages(conn, "m.chat_jid = ? AND m.ts > ?", (chat_jid, int(since.timestamp())))


def messages_after(db: Path, chat_jid: str, rowid: int) -> list[Message]:
    """Messages stored after a given row: the primitive `wareader follow` polls on."""
    with _connect(db) as conn:
        return _messages(conn, "m.chat_jid = ? AND m.rowid > ?", (chat_jid, rowid))


def last_rowid(db: Path) -> int:
    with _connect(db) as conn:
        return conn.execute("SELECT COALESCE(MAX(rowid), 0) FROM messages").fetchone()[0]


def push_names(db: Path, ids: set[str]) -> dict[str, str]:
    """The names people set on their own WhatsApp profile, as the bridge saw them."""
    if not ids:
        return {}
    marks = ",".join("?" * len(ids))
    with _connect(db) as conn:
        return dict(conn.execute(f"SELECT id, push_name FROM people WHERE id IN ({marks})", tuple(ids)).fetchall())


def group_index(db: Path) -> list[GroupInfo]:
    """Every joined group, most recently active first."""
    with _connect(db) as conn:
        rows = conn.execute(
            """
            SELECT g.jid, g.subject, g.kind, g.members, g.parent_jid, p.subject,
                   (SELECT COUNT(*) FROM messages m WHERE m.chat_jid = g.jid),
                   (SELECT MAX(ts) FROM messages m WHERE m.chat_jid = g.jid)
            FROM groups g LEFT JOIN groups p ON p.jid = g.parent_jid
            """
        ).fetchall()
    out = [GroupInfo(jid, subject, kind, members, parent_jid, parent, n, _ts(last)) for jid, subject, kind, members, parent_jid, parent, n, last in rows]
    out.sort(key=lambda g: (g.last_message or datetime.min.replace(tzinfo=timezone.utc), g.subject), reverse=True)
    return out


def status(db: Path, chat_jids: list[str]) -> tuple[dict[str, str], dict[str, tuple[int, datetime | None]]]:
    """Bridge state plus (message count, last message) per allowlisted group."""
    with _connect(db) as conn:
        state = dict(conn.execute("SELECT key, value FROM status").fetchall())
        per_group = {
            jid: conn.execute("SELECT COUNT(*), MAX(ts) FROM messages WHERE chat_jid = ?", (jid,)).fetchone()
            for jid in chat_jids
        }
    return state, {jid: (n, _ts(last)) for jid, (n, last) in per_group.items()}
