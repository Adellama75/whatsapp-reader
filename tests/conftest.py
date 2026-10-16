"""Synthetic live.db fixture, in the schema the bridge writes. Never add real chat content to tests."""

import sqlite3
from datetime import datetime, timezone

import pytest

GROUP = "120363000000000001@g.us"
OTHER = "120363000000000002@g.us"
COMMUNITY = "120363000000000003@g.us"

SCHEMA = """
CREATE TABLE groups (jid TEXT PRIMARY KEY, subject TEXT NOT NULL, kind TEXT NOT NULL,
    members INTEGER NOT NULL, parent_jid TEXT, updated_at INTEGER NOT NULL);
CREATE TABLE messages (chat_jid TEXT NOT NULL, id TEXT NOT NULL, sender TEXT NOT NULL, from_me INTEGER NOT NULL,
    ts INTEGER NOT NULL, kind TEXT NOT NULL, text TEXT NOT NULL, reply_to TEXT, PRIMARY KEY (chat_jid, id));
CREATE TABLE reactions (chat_jid TEXT NOT NULL, target_id TEXT NOT NULL, sender TEXT NOT NULL,
    emoji TEXT NOT NULL, ts INTEGER NOT NULL, PRIMARY KEY (chat_jid, target_id, sender));
CREATE TABLE people (id TEXT PRIMARY KEY, push_name TEXT NOT NULL, updated_at INTEGER NOT NULL);
CREATE TABLE status (key TEXT PRIMARY KEY, value TEXT NOT NULL);
"""


def ts(*args) -> int:
    return int(datetime(*args, tzinfo=timezone.utc).timestamp())


@pytest.fixture
def db(tmp_path):
    path = tmp_path / "live.db"
    conn = sqlite3.connect(path)
    conn.executescript(SCHEMA)
    conn.executemany("INSERT INTO groups VALUES (?,?,?,?,?,?)", [
        (GROUP, "Robotics Soc", "subgroup", 42, COMMUNITY, 1),
        (OTHER, "Other", "group", 5, None, 1),
        (COMMUNITY, "UCL Societies", "community", 300, None, 1),
    ])
    conn.executemany("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)", [
        (GROUP, "A", "447700900001", 0, ts(2026, 10, 1, 9), "text", "too old", None),
        (GROUP, "B", "447700900001", 0, ts(2026, 10, 3, 18), "text", "Social Thursday 7pm", None),
        (GROUP, "C", "", 1, ts(2026, 10, 3, 19), "text", "I'm in", "B"),
        (GROUP, "D", "447700900001", 0, ts(2026, 10, 3, 20), "image", "[image] poster", None),
        (OTHER, "E", "447700900001", 0, ts(2026, 10, 3, 20), "text", "not allowlisted", None),
        (GROUP, "F", "57153196953645", 0, ts(2026, 10, 3, 21), "text", "thanks @447700900001", None),
    ])
    # A thumbs-up from me on B, and a clap from the LID user (their latest reaction).
    conn.executemany("INSERT INTO reactions VALUES (?,?,?,?,?)", [
        (GROUP, "B", "", "👍", ts(2026, 10, 3, 18, 5)),
        (GROUP, "B", "57153196953645", "👏", ts(2026, 10, 3, 18, 7)),
    ])
    conn.execute("INSERT INTO people VALUES ('57153196953645', 'Bob', 1)")
    conn.executemany("INSERT INTO status VALUES (?,?)", [("state", "connected"), ("since", str(ts(2026, 10, 3, 8)))])
    conn.commit()
    conn.close()
    return path
