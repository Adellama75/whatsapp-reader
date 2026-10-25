"""Optional extensions installed next to whatsapp-reader, found through the `whatsapp_reader.plugins` entry-point
group. A plugin module may define:

- `register(subparsers)`: add subcommands (same `fn(config, args)` convention as cli.py);
- `AGENT_TOOLS`: extra `--allowedTools` patterns for `whatsapp-reader agent`;
- `AGENT_RULES`: the output section of the agent's prompt (see chatagent.core.posting_rules); without one the
  agent's output is the issues alone.

None are installed by default.
"""

from importlib.metadata import entry_points


def load() -> list:
    return [ep.load() for ep in entry_points(group="whatsapp_reader.plugins")]


def agent_tools() -> list[str]:
    return [t for p in load() for t in getattr(p, "AGENT_TOOLS", [])]


def agent_rules() -> str | None:
    return next((p.AGENT_RULES for p in load() if getattr(p, "AGENT_RULES", None)), None)
