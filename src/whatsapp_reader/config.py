import tomllib
from dataclasses import dataclass, field, fields
from pathlib import Path

from chatagent import Agent


@dataclass(frozen=True)
class Group:
    jid: str
    label: str
    # False: real names (push names, overridden by [names]), text unscrubbed.
    # Only for groups where everyone has agreed; the digest for such a group is sent to the API as-is.
    pseudonymise: bool = True
    save_media: bool = False  # the bridge downloads images/documents (<= 10 MB) to data/attachments; read by the bridge


@dataclass
class Config:
    secret_file: Path
    data_dir: Path
    model: str
    effort: str
    groups: list[Group]
    names: dict[str, str] = field(default_factory=dict)  # sender ID -> display name
    bridge: Path = Path("bridge/whatsapp-reader-bridge")
    agent: Agent | None = None  # chat-agent settings ([agent])
    raw: dict = field(default_factory=dict)  # the whole file, for plugins' own keys

    @property
    def live_db(self) -> Path:
        """Message archive written by the bridge; the CLI only ever reads it."""
        return self.data_dir / "live.db"



def load(path: Path) -> Config:
    """Relative paths in [paths] are taken relative to the config file, not the working directory,
    so `whatsapp-reader --config /x/config.toml` (or WHATSAPP_READER_CONFIG) works from anywhere."""
    raw = tomllib.loads(path.read_text())
    paths = raw["paths"]
    base = path.resolve().parent

    def rel(p: str) -> Path:
        q = Path(p).expanduser()
        return q if q.is_absolute() else base / q

    return Config(
        secret_file=rel(paths["secret_file"]),
        data_dir=rel(paths["data_dir"]),
        model=raw["summariser"]["model"],
        effort=raw["summariser"]["effort"],
        groups=[_known(Group, g) for g in raw.get("groups", [])],
        names=raw.get("names", {}),
        bridge=rel(paths.get("bridge", "bridge/whatsapp-reader-bridge")),
        agent=Agent.from_dict(raw.get("agent")),
        raw=raw,
    )


def _known(cls, d: dict):
    """Build a dataclass from the keys it knows; others (plugins' settings) stay in Config.raw."""
    names = {f.name for f in fields(cls)}
    return cls(**{k: v for k, v in d.items() if k in names})


