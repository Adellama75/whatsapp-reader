"""Whole CLI flow over a synthetic live.db. The Claude call is stubbed so this runs offline and free."""

import sqlite3
import sys

import pytest

from conftest import GROUP, ts

from wareader import cli
from wareader.summarise import Event, GroupDigest


@pytest.fixture
def run(db, tmp_path, monkeypatch, capsys):
    config = tmp_path / "config.toml"
    config.write_text(f"""
[paths]
secret_file = "{tmp_path / 'pseudonym.secret'}"
data_dir = "{db.parent}"

[summariser]
model = "claude-opus-5-5"
effort = "low"

[[groups]]
jid = "{GROUP}"
label = "Robotics Soc"
""")
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test")

    def _run(*args):
        monkeypatch.setattr(sys, "argv", ["wareader", "--config", str(config), *args])
        cli.main()
        return capsys.readouterr().out

    return _run


def test_read_only_commands(run, db):
    assert run("groups").strip() == f"{GROUP}\tRobotics Soc"
    index = run("index")
    assert "Robotics Soc\tsubgroup\t42\t5\t2026-10-03 21:00\tUCL Societies" in index and "\tyes" in index
    assert (db.parent / "groups.tsv").exists()
    assert run("status").startswith("bridge: connected since")

    t = run("transcript", "robotics soc", "--since", "2026-10-02")
    assert "Social Thursday 7pm" in t and "447700900001" not in t and "not allowlisted" not in t
    assert "No messages" in run("transcript", GROUP, "--days", "1")
    with pytest.raises(SystemExit, match="not an allowlisted group"):
        run("transcript", "chess club")


def test_summarise_sends_only_pseudonymised_text(run, monkeypatch):
    sent = []

    def fake_summarise(client, model, effort, group_label, transcript):
        sent.append(transcript)
        return GroupDigest(nothing_important=False, events=[Event(title="Social", when="Thursday 19:00", where=None)],
                           deadlines=[], announcements=[])

    monkeypatch.setattr("wareader.summarise.summarise", fake_summarise)
    dry = run("summarise", "Robotics Soc", "--since", "2026-10-02", "--dry-run")
    assert "Social Thursday 7pm" in dry and not sent
    out = run("summarise", "Robotics Soc", "--since", "2026-10-02")
    assert "**Social** (Thursday 19:00)" in out
    assert len(sent) == 1 and "447700900001" not in sent[0]


def test_follow_prints_messages_as_they_arrive(run, db, monkeypatch):
    # Nothing new on the first poll; then a message lands (as the bridge would write it) before the second.
    polls = iter(range(10))

    def arrive(_seconds):
        if next(polls) == 0:
            with sqlite3.connect(db) as conn:
                conn.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                             (GROUP, "G", "447700900001", 0, ts(2026, 10, 4, 9), "text", "room changed to 1.02", None))

    monkeypatch.setattr("wareader.cli.time.sleep", arrive)
    out = run("follow", "--polls", "2", "--interval", "0")
    assert out.strip() == "[Robotics Soc] [Sun 04 Oct 09:00] Member-" + out.strip().split("Member-")[1]
    assert "room changed to 1.02" in out and "Social Thursday" not in out
