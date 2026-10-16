import tomllib
from dataclasses import dataclass, field
from pathlib import Path


@dataclass(frozen=True)
class Group:
    jid: str
    label: str
    # False: real names from [names] (else the backup's masked number), text unscrubbed.
    # Only for groups where everyone has agreed; the digest for such a group is sent to the API as-is.
    pseudonymise: bool = True


@dataclass
class Config:
    backup: Path
    key_file: Path
    secret_file: Path
    data_dir: Path
    model: str
    effort: str
    groups: list[Group]
    phone: str | None = None  # scp source, e.g. galangal:/sdcard/.../msgstore.db.crypt15
    names: dict[str, str] = field(default_factory=dict)  # jid user (LID or number) -> display name

    @property
    def decrypted_db(self) -> Path:
        return self.data_dir / "msgstore.db"

    @property
    def state_file(self) -> Path:
        return self.data_dir / "state.json"


def load(path: Path) -> Config:
    raw = tomllib.loads(path.read_text())
    paths = raw["paths"]
    return Config(
        backup=Path(paths["backup"]).expanduser(),
        key_file=Path(paths["key_file"]).expanduser(),
        secret_file=Path(paths["secret_file"]).expanduser(),
        data_dir=Path(paths["data_dir"]).expanduser(),
        model=raw["summariser"]["model"],
        effort=raw["summariser"]["effort"],
        groups=[Group(**g) for g in raw.get("groups", [])],
        phone=paths.get("phone"),
        names=raw.get("names", {}),
    )
