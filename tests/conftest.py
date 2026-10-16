"""Synthetic msgstore.db fixture. Never add real chat content to tests."""

import sqlite3
from datetime import datetime, timezone

import pytest

GROUP = "120363000000000001@g.us"


def ms(*args) -> int:
    return int(datetime(*args, tzinfo=timezone.utc).timestamp() * 1000)


@pytest.fixture
def db(tmp_path):
    path = tmp_path / "msgstore.db"
    conn = sqlite3.connect(path)
    conn.executescript(
        """
        CREATE TABLE jid (_id INTEGER PRIMARY KEY, user TEXT, server TEXT, raw_string TEXT);
        CREATE TABLE chat (_id INTEGER PRIMARY KEY, jid_row_id INTEGER, subject TEXT);
        CREATE TABLE message (_id INTEGER PRIMARY KEY, chat_row_id INTEGER, from_me INTEGER,
                              sender_jid_row_id INTEGER, timestamp INTEGER, text_data TEXT);
        CREATE TABLE message_add_on (_id INTEGER PRIMARY KEY, chat_row_id INTEGER, from_me INTEGER,
                              sender_jid_row_id INTEGER, parent_message_row_id INTEGER, timestamp INTEGER);
        CREATE TABLE message_add_on_reaction (message_add_on_row_id INTEGER PRIMARY KEY, reaction TEXT);
        CREATE TABLE lid_display_name (lid_row_id INTEGER PRIMARY KEY, display_name TEXT);
        """
    )
    conn.executemany("INSERT INTO jid VALUES (?,?,?,?)", [
        (1, "120363000000000001", "g.us", GROUP),
        (2, "447700900001", "s.whatsapp.net", "447700900001@s.whatsapp.net"),
        (3, "120363000000000002", "g.us", "120363000000000002@g.us"),
        (4, "57153196953645", "lid", "57153196953645@lid"),
    ])
    conn.execute("INSERT INTO lid_display_name VALUES (4, '+44∙∙∙∙∙∙∙∙81')")
    conn.executemany("INSERT INTO chat VALUES (?,?,?)", [(1, 1, "Robotics Soc"), (2, 3, "Other")])
    conn.executemany("INSERT INTO message VALUES (?,?,?,?,?,?)", [
        (1, 1, 0, 2, ms(2026, 10, 1, 9), "too old"),
        (2, 1, 0, 2, ms(2026, 10, 3, 18), "Social Thursday 7pm"),
        (3, 1, 1, 0, ms(2026, 10, 3, 19), "I'm in"),
        (4, 1, 0, 2, ms(2026, 10, 3, 20), None),  # media, no text
        (5, 2, 0, 2, ms(2026, 10, 3, 20), "not allowlisted"),
        (6, 1, 0, 4, ms(2026, 10, 3, 21), "thanks @447700900001"),
    ])
    # Reactions: a thumbs-up from me on msg 2, and a heart from the LID user that was then
    # changed to a clap (latest wins), plus a removed reaction (empty) that must not show.
    conn.executemany("INSERT INTO message_add_on VALUES (?,?,?,?,?,?)", [
        (1, 1, 1, 0, 2, ms(2026, 10, 3, 18, 5)),
        (2, 1, 0, 4, 2, ms(2026, 10, 3, 18, 6)),
        (3, 1, 0, 4, 2, ms(2026, 10, 3, 18, 7)),
        (4, 1, 0, 2, 3, ms(2026, 10, 3, 19, 1)),
    ])
    conn.executemany("INSERT INTO message_add_on_reaction VALUES (?,?)", [
        (1, "👍"), (2, "❤️"), (3, "👏"), (4, ""),
    ])
    conn.commit()
    conn.close()
    return path
