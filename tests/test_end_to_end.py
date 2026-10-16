"""Whole CLI flow: encrypt a synthetic DB as WhatsApp would, then decrypt → extract → digest.

The Claude call is stubbed so this runs offline and free; everything else is real.
"""

import secrets
import subprocess
import sys

from conftest import GROUP

from wareader import cli
from wareader.summarise import Event, GroupDigest


def test_full_run(db, tmp_path, monkeypatch, capsys):
    key = secrets.token_hex(32)
    key_file = tmp_path / "backup.key"
    key_file.write_text(" ".join(key[i : i + 4] for i in range(0, 64, 4)))  # spaced, like WhatsApp shows it
    backup = tmp_path / "msgstore.db.crypt15"
    subprocess.run(["waencrypt", key, str(db), str(backup)], check=True, capture_output=True)

    data = tmp_path / "data"
    config = tmp_path / "config.toml"
    config.write_text(f"""
[paths]
backup = "{backup}"
key_file = "{key_file}"
secret_file = "{tmp_path / 'pseudonym.secret'}"
data_dir = "{data}"

[summariser]
model = "claude-opus-5-5"
effort = "low"

[[groups]]
jid = "{GROUP}"
label = "Robotics Soc"
""")

    sent = []

    def fake_summarise(client, model, effort, group_label, transcript):
        sent.append(transcript)
        return GroupDigest(
            nothing_important=False,
            events=[Event(title="Social", when="Thursday 19:00", where=None)],
            deadlines=[],
            announcements=[],
        )

    monkeypatch.setattr("wareader.summarise.summarise", fake_summarise)
    monkeypatch.setenv("ANTHROPIC_API_KEY", "test")

    def run(*args):
        monkeypatch.setattr(sys, "argv", ["wareader", "--config", str(config), *args])
        cli.main()
        return capsys.readouterr().out

    assert "Robotics Soc" in run("--decrypt", "list-groups")
    assert (data / "msgstore.db").exists()

    # Dry run sends nothing and leaves no state behind.
    dry = run("run", "--dry-run", "--days", "3650")
    assert "Social Thursday 7pm" in dry and "447700900001" not in dry
    assert not sent and not (data / "state.json").exists()

    out = run("run", "--days", "3650")
    assert "**Social** (Thursday 19:00)" in out
    assert len(sent) == 1 and "447700900001" not in sent[0] and "not allowlisted" not in sent[0]
    assert (data / "state.json").exists()

    # Second run: nothing new since the last run, so no API call and no clobbered digest.
    assert "Nothing new" in run("run")
    assert len(sent) == 1
    digest = next((data / "digests").glob("*.md")).read_text()
    assert "**Social** (Thursday 19:00)" in digest
