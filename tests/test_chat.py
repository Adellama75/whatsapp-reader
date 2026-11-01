"""`whatsapp-reader agent` / `follow` (chat-agent on the WhatsApp adapter) over the synthetic live.db. Claude Code is faked."""

import json
import sqlite3
import sys
from datetime import datetime, timezone
from pathlib import Path

import pytest

from conftest import GROUP, ts

from chatagent import core as ca_core
from chatagent import loop as ca_loop

from whatsapp_reader.chat import is_bot, replies_to_bot
from whatsapp_reader import cli
from whatsapp_reader import config as cfg
from whatsapp_reader.store import Message, messages_before, messages_since

AGENT = cfg.Agent(group="Robotics Soc", repo="org/repo", workdir=Path("."), debounce=10, max_wait=60, prefix="🤖")


def msg(text, sender="447700900001", rowid=1):
    return Message(GROUP, datetime(2026, 10, 4, 9, tzinfo=timezone.utc), sender, text, rowid=rowid)


def test_is_bot_only_matches_own_prefixed_posts():
    assert is_bot(msg("🤖 Opened #1", sender=None), "🤖")
    assert not is_bot(msg("🤖 Opened #1"), "🤖")  # someone else quoting the prefix
    assert not is_bot(msg("I'm in", sender=None), "🤖")


def test_messages_before_is_bounded_context(db):
    msgs = messages_since(db, GROUP, datetime(2026, 9, 1, tzinfo=timezone.utc))
    last = msgs[-1]
    assert [m.text for m in messages_before(db, GROUP, last.rowid, 2)] == ["I'm in", "[image] poster"]
    assert messages_before(db, GROUP, msgs[0].rowid, 5) == []


@pytest.fixture
def agent_run(db, tmp_path, monkeypatch, capsys):
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
pseudonymise = false

[agent]
group = "Robotics Soc"
repo = "org/repo"
workdir = "{tmp_path}"
debounce = 0
prefix = "🤖"

[agent.github_users]
Bob = "bob-gh"
""")

    def _run(*args):
        monkeypatch.setattr(sys, "argv", ["whatsapp-reader", "--config", str(config), *args])
        cli.main()
        return capsys.readouterr().out

    return _run


def test_agent_once_runs_claude_on_new_messages_and_skips_its_own(agent_run, db, monkeypatch):
    calls = []

    def fake_claude(a, system, prompt, tools=()):
        calls.append((system, prompt))
        return {"result": "Opened #1", "exit": 0, "seconds": 1.0, "total_cost_usd": 0.01}

    monkeypatch.setattr(ca_core, "run_claude", fake_claude)
    monkeypatch.setattr(ca_core, "list_issues", lambda a: "#1 [OPEN] Book the room")
    monkeypatch.setattr(ca_core.subprocess, "run", lambda *a, **k: ca_core.subprocess.CompletedProcess(a, 0, "", ""))  # the git pull

    # The cursor starts at "now": nothing pending on first start.
    assert agent_run("agent", "--once").strip() == "nothing new since the cursor"
    state = json.loads((db.parent / "agent.json").read_text())
    with sqlite3.connect(db) as conn:
        conn.executemany("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)", [
            (GROUP, "G", "57153196953645", 0, ts(2026, 10, 4, 9), "text", "I'll book the room by Friday", None),
            (GROUP, "H", "", 1, ts(2026, 10, 4, 9, 1), "text", "🤖 Opened #2 (not a real post, from an earlier run)", None),
        ])
    out = agent_run("agent", "--once")
    assert out.strip() == "Opened #1"
    system, prompt = calls[0]
    assert "org/repo" in system and "Bob -> bob-gh" in system
    assert "## New messages" in prompt and "Bob: I'll book the room by Friday" in prompt
    assert "🤖 Opened #2" not in prompt.split("## New messages")[1]  # own posts are never acted on
    assert "#1 [OPEN] Book the room" in prompt
    assert "Social Thursday 7pm" in prompt.split("## New messages")[0]  # earlier messages are context
    new_state = json.loads((db.parent / "agent.json").read_text())
    assert new_state["cursor"] > state["cursor"]
    log = [json.loads(l) for l in (db.parent / "agent.log").read_text().splitlines()]
    assert log[-1]["result"] == "Opened #1" and log[-1]["n"] == 2
    assert agent_run("agent", "--once").strip() == "nothing new since the cursor"


def test_agent_dry_run_prints_prompts_without_running(agent_run, db, monkeypatch):
    monkeypatch.setattr(ca_core, "run_claude", lambda *a: pytest.fail("must not run"))
    monkeypatch.setattr(ca_core, "list_issues", lambda a: "")
    monkeypatch.setattr(ca_core.subprocess, "run", lambda *a, **k: ca_core.subprocess.CompletedProcess(a, 0, "", ""))
    agent_run("agent", "--once")
    with sqlite3.connect(db) as conn:
        conn.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                     (GROUP, "G", "57153196953645", 0, ts(2026, 10, 4, 9), "text", "@bot what's open?", None))
    out = agent_run("agent", "--dry-run")
    assert "## New messages" in out and "@bot what's open?" in out


def test_mention_runs_without_waiting_for_quiet(agent_run, db, monkeypatch):
    seen = []
    monkeypatch.setattr(ca_core, "run_claude", lambda a, s, p, *t: seen.append(p) or {"result": "answered", "exit": 0})
    monkeypatch.setattr(ca_core, "list_issues", lambda a: "")
    monkeypatch.setattr(ca_core.subprocess, "run", lambda *a, **k: ca_core.subprocess.CompletedProcess(a, 0, "", ""))
    monkeypatch.setattr(ca_loop.time, "sleep", lambda s: None)
    agent_run("agent", "--once")
    with sqlite3.connect(db) as conn:
        conn.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                     (GROUP, "G", "57153196953645", 0, ts(2026, 10, 4, 9), "text", "@claude what's open?", None))
    monkeypatch.setattr(ca_core, "ready", lambda *a: False)  # pretend the chat is still busy
    agent_run("agent", "--polls", "1")
    assert len(seen) == 1 and "@claude what's open?" in seen[0]


def test_agent_pause_file_holds_the_batch(agent_run, db, monkeypatch):
    monkeypatch.setattr(ca_core, "run_claude", lambda *a: pytest.fail("paused: must not run"))
    monkeypatch.setattr(ca_core.subprocess, "run", lambda *a, **k: ca_core.subprocess.CompletedProcess(a, 0, "", ""))
    agent_run("agent", "--once")
    (db.parent / "agent.paused").touch()
    with sqlite3.connect(db) as conn:
        conn.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                     (GROUP, "G", "57153196953645", 0, ts(2026, 10, 4, 9), "text", "task for later", None))
    monkeypatch.setattr(ca_loop.time, "sleep", lambda s: None)
    agent_run("agent", "--polls", "3")
    cursor = json.loads((db.parent / "agent.json").read_text())["cursor"]
    (db.parent / "agent.paused").unlink()
    monkeypatch.setattr(ca_core, "run_claude", lambda *a: {"result": "ok", "exit": 0})
    monkeypatch.setattr(ca_core, "list_issues", lambda a: "")
    agent_run("agent", "--polls", "1")
    assert json.loads((db.parent / "agent.json").read_text())["cursor"] > cursor


def test_relative_config_paths_resolve_against_the_config_file(tmp_path, monkeypatch):
    (tmp_path / "config.toml").write_text('''
[paths]
secret_file = "~/.config/whatsapp-reader/pseudonym.secret"
data_dir = "data"
[summariser]
model = "m"
effort = "low"
''')
    monkeypatch.chdir(tmp_path.parent)  # somewhere else entirely
    c = cfg.load(tmp_path / "config.toml")
    assert c.data_dir == tmp_path / "data"
    assert c.bridge == tmp_path / "bridge" / "whatsapp-reader-bridge"
    assert c.secret_file.is_absolute() and c.secret_file.name == "pseudonym.secret"



def test_a_reply_to_the_bots_own_post_is_addressed_to_it():
    reply = msg("yes please")
    reply.quoted, reply.quoted_from_me = "🤖 Shall I open an issue?", True
    assert replies_to_bot(reply, "🤖")
    other = msg("yes please")
    other.quoted, other.quoted_from_me = "🤖 quoted by someone else", False
    assert not replies_to_bot(other, "🤖")
    assert not is_bot(msg("anything", sender=None), "")  # no prefix: nothing from Me counts as the agent's


def test_transcript_indents_continuations_marks_replies_and_names_me():
    from whatsapp_reader.pseudonymise import transcript
    m = msg("line one\nline two @100000000000001")
    m.id, m.quoted, m.quoted_from_me = "ABC", "🤖 earlier", True
    out = transcript(None, [m], names={"447700900001": "Bob"}, me={"100000000000001"},
                     to_claude=lambda x: replies_to_bot(x, "🤖"), ids=True)
    first, second = out.split("\n")
    assert first.startswith("[Sun 04 Oct 09:00 #ABC] Bob: [to claude] line one")
    assert second.startswith("    line two @Me")


def test_follow_resumes_from_its_cursor_and_beats_while_reading(agent_run, db, monkeypatch):
    monkeypatch.setattr(cli.time, "sleep", lambda s: None)
    beats = []
    real_touch = Path.touch
    monkeypatch.setattr(Path, "touch", lambda p, *a, **k: beats.append(p.name) or real_touch(p, *a, **k))
    agent_run("follow", "--after", "0", "--polls", "1")
    assert "coordinator.alive" in beats and not (db.parent / "coordinator.alive").exists()  # removed on exit
    saved = int((db.parent / "follow.cursor").read_text())
    with sqlite3.connect(db) as conn:
        conn.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                     (GROUP, "G", "57153196953645", 0, ts(2026, 10, 4, 9), "text", "arrived while nobody followed", None))
    out = agent_run("follow", "--polls", "1")  # no --after: picks up where the last one stopped
    assert "arrived while nobody followed" in out and "#G]" in out
    assert int((db.parent / "follow.cursor").read_text()) > saved


def test_a_live_coordinator_takes_the_replies(agent_run, db, monkeypatch):
    monkeypatch.setattr(ca_core, "run_claude", lambda *a: pytest.fail("coordinator answers mentions"))
    monkeypatch.setattr(ca_core.subprocess, "run", lambda *a, **k: ca_core.subprocess.CompletedProcess(a, 0, "", ""))
    monkeypatch.setattr(ca_loop.time, "sleep", lambda s: None)
    agent_run("agent", "--once")
    (db.parent / "coordinator.alive").touch()
    with sqlite3.connect(db) as conn:
        conn.execute("INSERT INTO messages VALUES (?,?,?,?,?,?,?,?)",
                     (GROUP, "G", "57153196953645", 0, ts(2026, 10, 4, 9), "text", "@claude what's open?", None))
    monkeypatch.setattr(ca_core, "ready", lambda *a: False)
    agent_run("agent", "--polls", "1")  # urgent, but the coordinator is alive: wait for the batch
    monkeypatch.setattr(ca_core, "list_issues", lambda a: "")
    out = agent_run("agent", "--dry-run")
    assert "WhatsApp chat \"Robotics Soc\"" in out and "Label `whatsapp`" in out and '"Me" in the transcript' in out
    assert "no way to write to the chat" in out  # no plugin: the issues are the whole output


def test_whatsapp_writes_are_read_only():
    """WhatsApp has no outbox here; every Chat write action refuses with ReadOnly."""
    from types import SimpleNamespace

    from chatagent import ReadOnly
    from whatsapp_reader.chat import WhatsAppChat

    c = SimpleNamespace(data_dir=Path("."))
    g = SimpleNamespace(label="Robotics Soc", jid="x@g.us")
    chat = WhatsAppChat(c, g, prefix="🤖", transcript=lambda *a, **k: "")
    for action in (lambda: chat.send("hi"),
                   lambda: chat.react("1", "\U0001f44d"),
                   lambda: chat.edit("1", "x"),
                   lambda: chat.delete("1")):
        with pytest.raises(ReadOnly):
            action()
