"""All data here is synthetic. Never add real chat content to tests."""

from datetime import date, datetime, timezone

from conftest import COMMUNITY, GROUP

from wareader.pseudonymise import label, scrub, transcript
from wareader.render import render
from wareader.store import group_index, messages_after, messages_since, push_names, status
from wareader.summarise import Deadline, Event, GroupDigest

SECRET = b"test-secret-not-real"
SINCE = datetime(2026, 10, 2, tzinfo=timezone.utc)


def test_messages_since_filters_by_group_and_time(db):
    msgs = messages_since(db, GROUP, SINCE)
    assert [(m.sender, m.text) for m in msgs] == [
        ("447700900001", "Social Thursday 7pm"),
        (None, "I'm in"),
        ("447700900001", "[image] poster"),
        ("57153196953645", "thanks @447700900001"),
    ]
    assert msgs[1].quoted == "Social Thursday 7pm"


def test_messages_after_is_a_cursor(db):
    first = messages_since(db, GROUP, SINCE)[0]
    assert [m.text for m in messages_after(db, GROUP, first.rowid)] == ["I'm in", "[image] poster", "thanks @447700900001"]
    assert messages_after(db, GROUP, 10**9) == []


def test_group_index_links_subgroups_to_their_community(db):
    by_jid = {g.jid: g for g in group_index(db)}
    assert by_jid[GROUP].kind == "subgroup" and by_jid[GROUP].parent == "UCL Societies"
    assert by_jid[GROUP].messages == 5 and by_jid[COMMUNITY].messages == 0
    assert [g.jid for g in group_index(db)][0] == GROUP  # most recently active first


def test_status(db):
    state, per_group = status(db, [GROUP])
    assert state["state"] == "connected"
    assert per_group[GROUP][0] == 5


def test_labels_are_stable_and_secret_dependent():
    assert label(SECRET, "447700900001") == label(SECRET, "447700900001")
    assert label(SECRET, "447700900001") != label(b"other", "447700900001")
    assert "447700900001" not in label(SECRET, "447700900001")


def test_scrub_mentions_phones_emails_but_keeps_dates():
    text = "@447700900001 call +44 7700 900002 or a.b@ucl.ac.uk, meet 2026-10-08 19:00"
    out = scrub(SECRET, text)
    assert out.startswith("@" + label(SECRET, "447700900001"))
    assert "[phone]" in out and "[email]" in out
    assert "2026-10-08 19:00" in out
    assert "7700" not in out


def test_scrub_strips_url_query_strings_and_fragments():
    text = (
        "see https://x.s3.amazonaws.com/a/b?X-Amz-Signature=abc123&E=1 and "
        "https://ucl.ac.uk/page#top ok"
    )
    assert scrub(SECRET, text) == "see https://x.s3.amazonaws.com/a/b and https://ucl.ac.uk/page ok"


def test_transcript_has_no_phone_numbers_and_shows_replies(db):
    msgs = messages_since(db, GROUP, SINCE)
    t = transcript(SECRET, msgs)
    assert "447700900001" not in t
    assert 'Me: I\'m in (replying to "Social Thursday 7pm")' in t


def test_render():
    md = render(date(2026, 10, 4), {
        "Robotics Soc": GroupDigest(
            nothing_important=False,
            events=[Event(title="Social", when="2026-10-08T19:00", where="Bloomsbury Tavern")],
            deadlines=[Deadline(what="Membership fee", due="2026-10-10")],
            announcements=["Elections next week"],
        ),
        "Course chat": GroupDigest(nothing_important=True, events=[], deadlines=[], announcements=[]),
    })
    assert "**Social** (2026-10-08T19:00 · Bloomsbury Tavern)" in md
    assert "Nothing important in: Course chat" in md


def test_reactions_attach_to_their_message(db):
    msgs = messages_since(db, GROUP, SINCE)
    by_text = {m.text: m for m in msgs}
    assert by_text["Social Thursday 7pm"].reactions == [(None, "👍"), ("57153196953645", "👏")]
    assert by_text["I'm in"].reactions == []
    assert "[👍 Me, 👏 Member-" in transcript(SECRET, msgs)


def test_named_transcript_uses_names_and_skips_scrubbing(db):
    msgs = messages_since(db, GROUP, SINCE)
    assert push_names(db, {"57153196953645", "447700900001"}) == {"57153196953645": "Bob"}
    t = transcript(None, msgs, names={"447700900001": "Alice", "57153196953645": "Bob"})
    assert "Bob: thanks @Alice" in t
    assert "Alice: Social Thursday 7pm [👍 Me, 👏 Bob]" in t
    assert "Member-" not in t
