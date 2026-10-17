"""Show read-only deployment evidence for a demonstration, without printing secrets."""
import json
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parents[1]
report = json.loads((root / '.local/vps-release-proof.json').read_text())
access = root / '.local/vps-access'
command = ['ssh', '-i', str(access / 'projectdesk_ed25519'), '-o', 'IdentitiesOnly=yes',
           '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes',
           '-o', 'UserKnownHostsFile=' + str(access / 'known_hosts'), '-o', 'ConnectTimeout=10',
           'root@' + report['vps'], 'bash -se']
script = '''
printf '=== VPS ===\\n'
hostname
uname -srm
printf '\\n=== CRM service ===\\n'
systemctl is-active projectdesk.service
systemctl is-enabled projectdesk.service
systemctl show projectdesk.service -p MainPID -p User -p WorkingDirectory
printf '\\n=== Health ===\\n'
curl --fail --silent http://127.0.0.1:8090/healthz
printf '\\n\\n=== CRM and Project MCP ===\\n'
python3 - <<'PYREMOTE'
import json,urllib.request,sqlite3
from pathlib import Path
settings=dict(line.split('=',1) for line in Path('/etc/projectdesk/crm.env').read_text().splitlines() if '=' in line and not line.startswith('#'))
token=settings['SUPPORT_ACCESS_TOKEN'].strip().strip('"')
request=urllib.request.Request('http://127.0.0.1:8090/api/status',headers={'Authorization':'Bearer '+token})
with urllib.request.urlopen(request,timeout=15) as response:
    status=json.load(response)
print(json.dumps({'CRM OS':status['serverOS'],'Project MCP':status['project'],'RAG index':status.get('info',{}).get('indexStatus'),'RAG chunks':status.get('info',{}).get('chunks'),'Model':status['model']},ensure_ascii=False,indent=2))
with sqlite3.connect('file:/var/lib/projectdesk/support.db?mode=ro',uri=True) as db:
    print(json.dumps({table:db.execute('SELECT count(*) FROM '+table).fetchone()[0] for table in ('users','tickets','messages')},indent=2))
PYREMOTE
printf '\\n=== Backups ===\\n'
systemctl is-active projectdesk-backup.timer
systemctl is-enabled projectdesk-backup.timer
'''
print('Connecting to VPS ' + report['vps'] + ' (read-only demonstration).', flush=True)
raise SystemExit(subprocess.run(command, input=script, text=True, timeout=40).returncode)
