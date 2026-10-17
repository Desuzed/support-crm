"""Install user LaunchAgents for Project MCP and the SSH tunnel. Contains no embedded secrets."""
import argparse
import os
import plistlib
import subprocess
import sys
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--project-root', type=Path, required=True)
parser.add_argument('--host')
parser.add_argument('--user')
parser.add_argument('--target', help='SSH config alias or user@host')
parser.add_argument('--key', type=Path, required=True)
parser.add_argument('--known-hosts', type=Path, required=True)
parser.add_argument('--local-port', type=int, default=8090)
parser.add_argument('--tunnel-only', action='store_true', help='Preserve the already running project MCP service')
args = parser.parse_args()
if args.target and (args.host or args.user):
    parser.error('Use either --target or --host with --user')
if not args.target and not (args.host and args.user):
    parser.error('Specify --target or both --host and --user')
if not 0 <= args.local_port <= 65535:
    parser.error('Invalid local port')
root = Path(__file__).resolve().parents[1]
project = args.project_root.resolve()
if not (project / '.local/support-project-mcp').exists():
    raise SystemExit('Build the project MCP executable first')
agents = Path.home() / 'Library/LaunchAgents'
agents.mkdir(parents=True, exist_ok=True)
domain = 'gui/' + str(os.getuid())
tunnel = [sys.executable, str(root / 'scripts/start_tunnel.py'), '--key', str(args.key.resolve()), '--known-hosts', str(args.known_hosts.resolve()), '--local-port', str(args.local_port)]
tunnel += ['--target', args.target] if args.target else ['--host', args.host, '--user', args.user]
definitions = {}
if not args.tunnel_only:
    definitions['local.projectdesk.project-mcp'] = [sys.executable, str(root / 'scripts/run_project_bridge.py'), '--project-root', str(project)]
definitions['local.projectdesk.tunnel'] = tunnel
for label, command in definitions.items():
    target = agents / (label + '.plist')
    data = {'Label': label, 'ProgramArguments': command, 'WorkingDirectory': str(root), 'RunAtLoad': True, 'KeepAlive': True, 'ThrottleInterval': 10, 'ProcessType': 'Background', 'StandardOutPath': str(root / '.local' / (label + '.log')), 'StandardErrorPath': str(root / '.local' / (label + '.log'))}
    subprocess.run(['launchctl', 'bootout', domain + '/' + label], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    with target.open('wb') as output:
        plistlib.dump(data, output)
    target.chmod(0o644)
    subprocess.run(['launchctl', 'bootstrap', domain, str(target)], check=True)
    print('Installed user agent: ' + label)
