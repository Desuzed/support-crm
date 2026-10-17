"""Create a consistent SQLite backup while CRM is running; retain 14 snapshots."""
from datetime import datetime, timezone
import os
from pathlib import Path
import sqlite3


def backup(database, directory):
    os.umask(0o077)
    database, directory = Path(database), Path(directory)
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    name = 'support-' + datetime.now(timezone.utc).strftime('%Y%m%d-%H%M%S-%f') + '.db'
    target = directory / name
    temporary = directory / (name + '.tmp')
    try:
        with sqlite3.connect(database.resolve().as_uri() + '?mode=ro', uri=True, timeout=30) as source:
            with sqlite3.connect(temporary) as copy:
                source.backup(copy, pages=128, sleep=0.1)
                if copy.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                    raise RuntimeError('SQLite backup integrity check failed')
        temporary.chmod(0o600)
        temporary.replace(target)
        for old in sorted(directory.glob('support-*.db'), reverse=True)[14:]:
            old.unlink()
    finally:
        if temporary.exists():
            temporary.unlink()
    return target


if __name__ == '__main__':
    print(backup('/var/lib/projectdesk/support.db', '/var/lib/projectdesk/backups'))
