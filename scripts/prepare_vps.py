"""Prepare a private Linux CRM bundle; does not connect to or change any server."""
import argparse
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--arch', choices=['amd64', 'arm64'], required=True)
    parser.add_argument('--database', type=Path, help='Consistent SQLite snapshot taken during Windows cutover')
    parser.add_argument('--http-tunnel', action='store_true', help='Private localhost browser access without HTTPS; disable Secure cookies')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    # Whitelist settings; unrelated project secrets are never bundled.
    values = dict(line.split('=', 1) for line in (root / '.env').read_text().splitlines()
                  if '=' in line and not line.startswith('#'))
    config = {name: values.get(name, '').strip().strip('\"\'') for name in
              ['SUPPORT_ACCESS_TOKEN', 'DEEPSEEK_API_KEY', 'DEEPSEEK_MODEL', 'PROJECT_MCP_TOKEN']}
    for name in ['SUPPORT_ACCESS_TOKEN', 'PROJECT_MCP_TOKEN']:
        if len(config[name]) < 32:
            parser.error(name + ' must contain at least 32 characters')
    if not config['DEEPSEEK_API_KEY']:
        parser.error('DEEPSEEK_API_KEY is not configured')
    config.update(SUPPORT_ADDR='127.0.0.1:8090', SUPPORT_DB='/var/lib/projectdesk/support.db',
                  PROJECT_MCP_URL='http://127.0.0.1:18091/mcp', SUPPORT_SEED_DEMO='false',
                  SUPPORT_SECURE_COOKIES='false' if args.http_tunnel else 'true')
    for value in config.values():
        if any(char in value for char in '\n\r\x00'):
            parser.error('Invalid multiline configuration value')
    stage = root / '.local/vps' / args.arch
    stage.mkdir(mode=0o700, parents=True, exist_ok=True)
    stage.chmod(0o700)
    environment = {**os.environ, 'GOOS': 'linux', 'GOARCH': args.arch, 'CGO_ENABLED': '0'}
    subprocess.run(['go', 'build', '-trimpath', '-o', str(stage / 'support-crm'), '.'],
                   cwd=root, env=environment, check=True)
    settings = stage / 'crm.env'
    # systemd reads EnvironmentFile without shell expansion. Escape only double quotes
    # and backslashes; JSON's other escape sequences are not EnvironmentFile syntax.
    def quote(value):
        if any(char.isspace() or char in '\"\\\'' for char in value):
            return '\"' + value.replace('\\', '\\\\').replace('\"', '\\\"') + '\"'
        return value
    settings.write_text(''.join(name + '=' + quote(value) + '\n' for name, value in config.items()))
    settings.chmod(0o600)
    for filename in ['projectdesk.service', 'install.sh', 'Caddyfile.example',
                     'backup.py', 'projectdesk-backup.service', 'projectdesk-backup.timer']:
        shutil.copy2(root / 'deploy/vps' / filename, stage / filename)
    database = stage / 'support.db'
    if args.database:
        source = args.database.resolve(strict=True)
        if source == database.resolve():
            parser.error('Database source must be outside the prepared bundle')
        if database.exists():
            database.unlink()
        with sqlite3.connect(source.as_uri() + '?mode=ro', uri=True) as original:
            if original.execute('PRAGMA integrity_check').fetchone()[0] != 'ok':
                parser.error('Source SQLite integrity check failed')
            with sqlite3.connect(database) as copy:
                original.backup(copy)
        database.chmod(0o600)
    elif database.exists():
        # A previous bundle's migration data must never be sent by accident.
        database.unlink()
    print('Prepared private CRM bundle: ' + str(stage))
    print('Includes database snapshot: ' + str(bool(args.database)))
    print('No server was changed. RAG and Project MCP remain on Mac.')


if __name__ == '__main__':
    main()
