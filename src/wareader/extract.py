"""Read group messages out of a decrypted Android msgstore.db.

The schema is undocumented and shifts between WhatsApp versions. These queries
target the post-2022 layout (`message`, `chat`, `jid` tables); check them against
your own DB with `sqlite3 data/msgstore.db .schema` if anything comes back empty.
"""

import sqlite3
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path


@dataclass
class Message:
    chat_jid: str
    sent_at: datetime
    sender: str | None  # jid user part (phone number or LID); None means you sent it
    text: str
    reactions: list[tuple[str | None, str]] = field(default_factory=list)  # (sender, emoji)


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
            SELECT m._id, m.timestamp, m.from_me, sender.user, m.text_data
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
        # Reactions live in message_add_on, pointing at the message they decorate.
        # A sender's latest reaction wins; an empty reaction means it was removed.
        reactions: dict[int, dict[str | None, str]] = {}
        for parent, from_me, user, emoji in conn.execute(
            """
            SELECT a.parent_message_row_id, a.from_me, sender.user, r.reaction
            FROM message_add_on a
            JOIN message_add_on_reaction r ON r.message_add_on_row_id = a._id
            JOIN chat ON chat._id = a.chat_row_id
            JOIN jid AS chat_jid ON chat_jid._id = chat.jid_row_id
            LEFT JOIN jid AS sender ON sender._id = a.sender_jid_row_id
            WHERE chat_jid.raw_string = ? AND a.timestamp > ?
            ORDER BY a.timestamp
            """,
            (chat_jid, since_ms),
        ):
            reactions.setdefault(parent, {})[None if from_me else user] = emoji
    return [
        Message(
            chat_jid=chat_jid,
            sent_at=datetime.fromtimestamp(ts / 1000, tz=timezone.utc),
            sender=None if from_me else user,
            text=text,
            reactions=[(who, e) for who, e in reactions.get(mid, {}).items() if e],
        )
        for mid, ts, from_me, user, text in rows
    ]


def display_names(db: Path, users: set[str]) -> dict[str, str]:
    """Whatever the backup knows about these senders: usually just a masked number."""
    if not users:
        return {}
    marks = ",".join("?" * len(users))
    with sqlite3.connect(f"file:{db}?mode=ro", uri=True) as conn:
        return dict(
            conn.execute(
                f"""
                SELECT jid.user, d.display_name
                FROM lid_display_name d JOIN jid ON jid._id = d.lid_row_id
                WHERE jid.user IN ({marks})
                """,
                tuple(users),
            ).fetchall()
        )


# chat.group_type as observed in a 2026 backup (undocumented; verify against yours).
GROUP_KINDS = {0: "group", 1: "announcements", 2: "subgroup", 3: "community", 6: "subgroup"}


@dataclass
class GroupInfo:
    jid: str
    subject: str
    kind: str
    members: int
    messages: int
    last_message: datetime | None
    parent: str | None  # subject of the most likely community, for subgroups
    parent_jid: str | None
    shared: int  # members this subgroup shares with that community


def group_index(db: Path) -> list[GroupInfo]:
    """Every group with its kind, size, activity and (for subgroups) likely community.

    The backup stores no direct subgroup -> community link, so the community is
    inferred as the community whose member list overlaps this group's the most.
    """
    with sqlite3.connect(f"file:{db}?mode=ro", uri=True) as conn:
        rows = conn.execute(
            """
            SELECT chat._id, jid.raw_string, chat.subject, chat.group_type,
                   (SELECT COUNT(*) FROM group_participant_user g WHERE g.group_jid_row_id = chat.jid_row_id),
                   (SELECT COUNT(*) FROM message m WHERE m.chat_row_id = chat._id),
                   (SELECT MAX(m.timestamp) FROM message m WHERE m.chat_row_id = chat._id)
            FROM chat JOIN jid ON jid._id = chat.jid_row_id
            WHERE jid.server = 'g.us'
            """
        ).fetchall()
        overlaps = conn.execute(
            """
            SELECT sj.raw_string, cj.raw_string, COUNT(*)
            FROM group_participant_user a
            JOIN group_participant_user b ON a.user_jid_row_id = b.user_jid_row_id
            JOIN chat sc ON sc.jid_row_id = a.group_jid_row_id
            JOIN chat cc ON cc.jid_row_id = b.group_jid_row_id
            JOIN jid sj ON sj._id = a.group_jid_row_id
            JOIN jid cj ON cj._id = b.group_jid_row_id
            WHERE sc.group_type IN (2, 6) AND cc.group_type = 3
            GROUP BY 1, 2
            """
        ).fetchall()

    subjects = {jid: subject for _, jid, subject, *_ in rows}
    best: dict[str, tuple[str, int]] = {}
    for sub, community, n in overlaps:
        if n > best.get(sub, ("", 0))[1]:
            best[sub] = (community, n)

    out = []
    for _, jid, subject, gtype, members, msgs, last_ms in rows:
        parent_jid, shared = best.get(jid, (None, 0))
        out.append(
            GroupInfo(
                jid=jid,
                subject=subject or "",
                kind=GROUP_KINDS.get(gtype, f"type{gtype}"),
                members=members,
                messages=msgs,
                last_message=datetime.fromtimestamp(last_ms / 1000, tz=timezone.utc) if last_ms else None,
                parent=subjects.get(parent_jid) if parent_jid else None,
                parent_jid=parent_jid,
                shared=shared,
            )
        )
    out.sort(key=lambda g: (g.last_message or datetime.min.replace(tzinfo=timezone.utc)), reverse=True)
    return out
