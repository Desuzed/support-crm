$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
if (!(Test-Path '.env')) { throw 'Create .env from .env.example before starting.' }
if (!(Test-Path 'support-crm.exe')) { throw 'Place support-crm.exe in the application directory.' }
& '.\support-crm.exe'
exit $LASTEXITCODE
