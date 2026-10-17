"""Deploy only the executable and local configuration to an explicitly selected Windows host."""
import argparse
import base64
import json
import subprocess
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--host', required=True)
parser.add_argument('--user', required=True)
parser.add_argument('--key', type=Path, required=True)
parser.add_argument('--known-hosts', type=Path, required=True)
args = parser.parse_args()
root = Path(__file__).resolve().parents[1]
ssh_options = ['-i', str(args.key.resolve()), '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes', '-o', 'StrictHostKeyChecking=yes', '-o', 'UserKnownHostsFile=' + str(args.known_hosts.resolve()), '-o', 'ConnectTimeout=10']
destination = args.user + '@' + args.host

def powershell(script, timeout=60):
    encoded = base64.b64encode(script.encode('utf-16le')).decode('ascii')
    result = subprocess.run(['ssh', *ssh_options, destination, 'powershell.exe -NoProfile -NonInteractive -EncodedCommand ' + encoded], capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError('Windows deployment command failed: ' + result.stderr.decode(errors='replace')[:1500])
    return result.stdout.decode(errors='replace').strip()

binary = root / 'dist/support-crm.exe'
if not binary.exists():
    raise SystemExit('Build dist/support-crm.exe first')
values = dict(line.split('=', 1) for line in (root / '.env').read_text().splitlines() if '=' in line and not line.startswith('#'))
values['SUPPORT_ADDR'] = '127.0.0.1:8090'
values['SUPPORT_DB'] = '.local/support.db'
values['PROJECT_MCP_URL'] = 'http://127.0.0.1:18091/mcp'
stage = root / '.local/windows-deploy'
stage.mkdir(parents=True, exist_ok=True)
remote_env = stage / 'support.env'
remote_env.write_text(''.join(k + '=' + v + '\n' for k, v in values.items()))
remote_env.chmod(0o600)

remote_dir = powershell("$ErrorActionPreference='Stop'; $dir=Join-Path $env:USERPROFILE 'Apps\\ProjectDesk'; New-Item -ItemType Directory -Force $dir | Out-Null; $dir.Replace('\\','/')")
if not remote_dir or '\n' in remote_dir or '..' in remote_dir:
    raise SystemExit('Unexpected remote directory')
# Stop only this application before replacing a locked Windows executable.
powershell("$ErrorActionPreference='Stop'; $dir=Join-Path $env:USERPROFILE 'Apps\\ProjectDesk'; $exe=Join-Path $dir 'support-crm.exe'; Get-CimInstance Win32_Process -Filter \"Name='support-crm.exe'\" | Where-Object {$_.ExecutablePath -eq $exe} | ForEach-Object {Stop-Process -Id $_.ProcessId -Force}; if (Get-ScheduledTask -TaskName 'ProjectDeskSupport' -ErrorAction SilentlyContinue) {Stop-ScheduledTask -TaskName 'ProjectDeskSupport'}")
for source, name in [(binary, 'support-crm.exe'), (remote_env, '.env'), (root / '.env.example', '.env.example'), (root / 'README.md', 'README.md')]:
    subprocess.run(['scp', *ssh_options, str(source), destination + ':' + remote_dir + '/' + name], check=True, timeout=120)

script = r'''
$ErrorActionPreference='Stop'
$dir=Join-Path $env:USERPROFILE 'Apps\ProjectDesk'
$account=[Security.Principal.WindowsIdentity]::GetCurrent().Name
# Keep the DeepSeek key and CRM data accessible only to this user, SYSTEM and administrators.
& icacls.exe $dir /inheritance:r /grant:r "${account}:(OI)(CI)F" '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' /Q | Out-Null
if ($LASTEXITCODE -ne 0) {throw 'Cannot restrict application directory permissions'}
New-Item -ItemType Directory -Force (Join-Path $dir '.local') | Out-Null
$action=New-ScheduledTaskAction -Execute (Join-Path $dir 'support-crm.exe') -WorkingDirectory $dir
$trigger=New-ScheduledTaskTrigger -AtStartup
$principal=New-ScheduledTaskPrincipal -UserId $account -LogonType S4U -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName 'ProjectDeskSupport' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName 'ProjectDeskSupport'
$ok=$false
for ($i=0;$i -lt 20;$i++) {
    try {$health=Invoke-RestMethod -Uri 'http://127.0.0.1:8090/healthz' -TimeoutSec 2; if ($health.status -eq 'ok') {$ok=$true;break}} catch {}
    Start-Sleep -Milliseconds 500
}
if (!$ok) {throw ('Support health check failed; task result: '+(Get-ScheduledTaskInfo -TaskName 'ProjectDeskSupport').LastTaskResult)}
[ordered]@{installed=$true;directory=$dir;task=(Get-ScheduledTask -TaskName 'ProjectDeskSupport').State.ToString();health=$health.status;address='127.0.0.1:8090'} | ConvertTo-Json -Compress
'''
summary = powershell(script)
print(summary)
(stage / 'deployment-result.json').write_text(summary + '\n')
