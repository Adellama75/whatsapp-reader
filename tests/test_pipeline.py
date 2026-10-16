"""All data here is synthetic. Never add real chat content to tests."""

from datetime import date, datetime, timezone

from conftest import GROUP

from wareader.extract import list_groups, messages_since
from wareader.pseudonymise import label, scrub, transcript
from wareader.render import render
from wareader.summarise import Deadline, Event, GroupDigest

SECRET = b"test-secret-not-real"


def test_list_groups(db):
    assert list_groups(db) == [("120363000000000002@g.us", "Other"), (GROUP, "Robotics Soc")]


def test_messages_since_filters_by_group_time_and_text(db):
    msgs = messages_since(db, GROUP, datetime(2026, 10, 2, tzinfo=timezone.utc))
    assert [(m.sender, m.text) for m in msgs] == [("447700900001", "Social Thursday 7pm"), (None, "I'm in")]


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


def test_transcript_has_no_phone_numbers(db):
    msgs = messages_since(db, GROUP, datetime(2026, 10, 2, tzinfo=timezone.utc))
    t = transcript(SECRET, msgs)
    assert "447700900001" not in t
    assert "Me: I'm in" in t


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
