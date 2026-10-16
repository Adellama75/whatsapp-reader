import tomllib
from dataclasses import dataclass, field
from pathlib import Path


@dataclass(frozen=True)
class Group:
    jid: str
    label: str
    # False: real names (push names, overridden by [names]), text unscrubbed.
    # Only for groups where everyone has agreed; the digest for such a group is sent to the API as-is.
    pseudonymise: bool = True


@dataclass
class Config:
    secret_file: Path
    data_dir: Path
    model: str
    effort: str
    groups: list[Group]
    names: dict[str, str] = field(default_factory=dict)  # sender ID -> display name
    bridge: Path = Path("bridge/wareader-bridge")

    @property
    def live_db(self) -> Path:
        """Message archive written by the bridge; the CLI only ever reads it."""
        return self.data_dir / "live.db"


def load(path: Path) -> Config:
    raw = tomllib.loads(path.read_text())
    paths = raw["paths"]
    return Config(
        secret_file=Path(paths["secret_file"]).expanduser(),
        data_dir=Path(paths["data_dir"]).expanduser(),
        model=raw["summariser"]["model"],
        effort=raw["summariser"]["effort"],
        groups=[Group(**g) for g in raw.get("groups", [])],
        names=raw.get("names", {}),
        bridge=Path(paths.get("bridge", "bridge/wareader-bridge")).expanduser(),
    )
