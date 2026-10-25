import anthropic
from pydantic import BaseModel


class Event(BaseModel):
    title: str
    when: str | None  # ISO 8601 if the chat gives a clear date/time, else as written
    where: str | None


class Deadline(BaseModel):
    what: str
    due: str | None


class GroupDigest(BaseModel):
    nothing_important: bool
    events: list[Event]
    deadlines: list[Deadline]
    announcements: list[str]


SYSTEM = """You summarise a university society or course WhatsApp group for a busy \
student who doesn't read it. Participants are pseudonymised (Member-xxxx); keep them \
that way and never guess real identities.

Extract only what the reader would act on or regret missing: events (with date, time \
and place when stated), deadlines, and announcements such as rule changes, sign-ups \
or cancellations. Skip chit-chat, memes and "anyone going?" threads unless they \
settle a plan. If a later message changes an earlier plan, report only the final \
version. Resolve relative dates ("next Thursday") against the message timestamps. \
If nothing qualifies, set nothing_important to true and leave the lists empty."""


def summarise(
    client: anthropic.Anthropic, model: str, effort: str, group_label: str, transcript: str
) -> GroupDigest:
    response = client.beta.messages.parse(
        model=model,
        max_tokens=16000,
        system=SYSTEM,
        messages=[{"role": "user", "content": f"Group: {group_label}\n\n{transcript}"}],
        output_config={"effort": effort},
        output_format=GroupDigest,
        betas=["server-side-fallback-2026-07-01"],
        fallbacks="default",
    )
    if response.stop_reason == "refusal":
        raise RuntimeError(f"{group_label}: model declined to summarise")
    return response.parsed_output
