"""Keep localhost-only CRM and Project MCP connected over the existing SSH account."""
import argparse
import subprocess
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--host', help='Legacy host; use --target for an SSH config alias')
parser.add_argument('--user')
parser.add_argument('--target', help='SSH config alias or user@host; honors local SSH configuration')
parser.add_argument('--key')
parser.add_argument('--known-hosts')
parser.add_argument('--ssh-port', type=int)
parser.add_argument('--local-port', type=int, default=8090, help='Mac browser port; use 0 for reverse MCP forwarding only')
args = parser.parse_args()
if args.target and (args.host or args.user):
    parser.error('Use either --target or --host with --user')
if not args.target and not (args.host and args.user):
    parser.error('Specify --target or both --host and --user')
target = args.target or args.user + '@' + args.host
if target.startswith('-'):
    parser.error('SSH target cannot start with -')
if not 0 <= args.local_port <= 65535 or (args.ssh_port is not None and not 1 <= args.ssh_port <= 65535):
    parser.error('Invalid port')
command = ['ssh', '-N', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'ExitOnForwardFailure=yes', '-o', 'ServerAliveInterval=15', '-o', 'ServerAliveCountMax=3']
if args.key:
    command += ['-i', args.key, '-o', 'IdentitiesOnly=yes']
if args.known_hosts:
    command += ['-o', 'UserKnownHostsFile=' + args.known_hosts]
if args.ssh_port:
    command += ['-p', str(args.ssh_port)]
command += ['-R', '127.0.0.1:18091:127.0.0.1:8091']
if args.local_port:
    command += ['-L', f'127.0.0.1:{args.local_port}:127.0.0.1:8090']
command += [target]
raise SystemExit(subprocess.call(command))
