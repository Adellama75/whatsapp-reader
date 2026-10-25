"""The chat-agent adapter for one allowlisted group: `whatsapp-reader agent` and `follow` run chat-agent on it."""

from chatagent import Msg

from . import config as cfg
from . import plugins
from .store import Message, last_rowid, messages_after, messages_before


def is_bot(m: Message, prefix: str) -> bool:
    """A message the agent itself posted: from this account and carrying the prefix."""
    return m.sender is None and bool(prefix) and m.text.startswith(prefix)


def replies_to_bot(m: Message, prefix: str) -> bool:
    """A WhatsApp reply to one of the agent's own posts (same test as is_bot, on the quoted message)."""
    return m.quoted_from_me and bool(prefix) and (m.quoted or "").startswith(prefix)


class WhatsAppChat:
    platform, issue_label = "WhatsApp", "whatsapp"
    who_is_me = '"Me" in the transcript is the person who runs this bridge.'
    names: tuple[str, ...] = ()

    def __init__(self, c: cfg.Config, g: cfg.Group, prefix: str, transcript):
        self.c, self.g, self.prefix, self.transcript = c, g, prefix, transcript
        self.label, self.data_dir = g.label, c.data_dir
        self.attachments = str(c.data_dir / "attachments")
        self.tools = plugins.agent_tools()
        self.rules = plugins.agent_rules()  # None: issues only (the bridge never posts)

    def _msg(self, m: Message) -> Msg:
        return Msg(m.rowid, m.text, is_bot(m, self.prefix), replies_to_bot(m, self.prefix), m)

    def poll(self) -> None:
        pass  # the bridge writes live.db as messages arrive

    def after(self, cursor: int) -> list[Msg]:
        return [self._msg(m) for m in messages_after(self.c.live_db, self.g.jid, cursor)]

    def before(self, cursor: int, n: int) -> list[Msg]:
        return [self._msg(m) for m in messages_before(self.c.live_db, self.g.jid, cursor, n)]

    def last(self) -> int:
        return last_rowid(self.c.live_db)

    def render(self, msgs: list[Msg], to_agent=None, ids: bool = False) -> str:
        flag = (lambda m: replies_to_bot(m, self.prefix)) if to_agent else None
        return self.transcript(self.c, self.g, [m.raw for m in msgs], to_claude=flag, ids=ids)

    def side_events(self) -> list[str]:
        return []
