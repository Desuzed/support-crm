param(
    [Parameter(Mandatory=$true)][string]$Target,
    [Parameter(Mandatory=$true)][string]$KeyPath,
    [Parameter(Mandatory=$true)][string]$KnownHostsPath,
    [ValidateRange(1,65535)][int]$LocalPort=8090
)
$ErrorActionPreference='Stop'
$ssh=Join-Path $env:WINDIR 'System32\OpenSSH\ssh.exe'
if (!(Test-Path $ssh) -or !(Test-Path $KeyPath) -or !(Test-Path $KnownHostsPath)) {
    throw 'SSH client, private key or pinned known_hosts is missing'
}
if ($Target.StartsWith('-') -or $Target -match '[\r\n\s]') {throw 'Invalid SSH target'}
$log=Join-Path $PSScriptRoot 'ssh-client.log'
$sshArguments=@('-N','-T','-i',$KeyPath,'-E',$log,
    '-o','IdentitiesOnly=yes','-o','BatchMode=yes','-o','StrictHostKeyChecking=yes',
    '-o',('UserKnownHostsFile="'+$KnownHostsPath+'"'),
    '-o','ExitOnForwardFailure=yes','-o','ConnectTimeout=10',
    '-o','ServerAliveInterval=15','-o','ServerAliveCountMax=3',
    '-L',('127.0.0.1:'+ $LocalPort +':127.0.0.1:8090'),$Target)
while ($true) {
    try {& $ssh @sshArguments}
    catch {[IO.File]::AppendAllText($log,([DateTime]::UtcNow.ToString('o')+' SSH client failed: '+$_.Exception.Message+"`n"))}
    Start-Sleep -Seconds 10
}
