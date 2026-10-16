import subprocess
from pathlib import Path


def decrypt(backup: Path, key_file: Path, out: Path) -> None:
    """Decrypt msgstore.db.crypt15 with wa-crypt-tools' `wadecrypt` CLI.

    `key_file` holds the 64-digit key exactly as WhatsApp shows it (spaces are fine).
    """
    key = "".join(key_file.read_text().split())
    out.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run(["wadecrypt", key, str(backup), str(out)], check=True)
