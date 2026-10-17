<#
.SYNOPSIS
    Checks that no doctor tool hands secret values to the model.

.DESCRIPTION
    Plants fake secrets in a throwaway repo folder (.env, a credentials file
    under secrets\, a password in a URL, a key in a log line, an environment
    variable) and calls the tools the way a model would, including the
    workarounds a model might try. Fails if any planted value appears in any
    output, or if a non-secret value the doctor needs is hidden.

    Then repeats the main checks against this repo's real .env and running
    containers (docker inspect, docker compose config), without printing any
    secret value.

    No model is involved. Exit code 0 when every check passes.
#>
[CmdletBinding()]
param([switch]$SkipRealRepo)

$ErrorActionPreference = 'Stop'
$realRepo = (Resolve-Path (Join-Path $PSScriptRoot '..\..\..\..')).Path

function New-Fake([string]$Prefix, [int]$Length = 40) {
    $chars = [char[]]'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789'
    $Prefix + (-join (1..$Length | ForEach-Object { $chars[(Get-Random -Maximum $chars.Length)] }))
}

# Built at run time so this file holds nothing a secret scanner would flag.
$fake = @{
    AnthropicKey = New-Fake ('sk-' + 'ant-api03-') 60
    MongoPass    = New-Fake '' 32
    SmtpPass     = New-Fake '' 16
    TokenSecret  = New-Fake '' 64
    CredsFile    = New-Fake 'oauth-refresh-' 30
    EnvVarToken  = New-Fake 'tok_' 30
    LoggedKey    = New-Fake ('x' + 'ai-') 50
}

$tmp = Join-Path $env:TEMP ("doctor-secret-guard-" + [guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Force (Join-Path $tmp 'secrets'), (Join-Path $tmp 'output') | Out-Null
@"
MONGO_ROOT_PASSWORD=$($fake.MongoPass)
MONGO_URL=mongodb://root:$($fake.MongoPass)@mongo:27017/admin
ANTHROPIC_API_KEY="$($fake.AnthropicKey)"
SMTP_PORT=587
SMTP_PASSWORD=$($fake.SmtpPass)
MADSCIENTIST_TOKEN_SECRET=$($fake.TokenSecret)
DECISION_TOKEN_TTL_SECONDS=86400
OPENAI_API_KEY=
"@ | Set-Content (Join-Path $tmp '.env') -Encoding UTF8
"MONGO_ROOT_PASSWORD=change-me" | Set-Content (Join-Path $tmp '.env.example') -Encoding UTF8
"{`"refresh_token`": `"$($fake.CredsFile)`"}" | Set-Content (Join-Path $tmp 'secrets\gmail.json') -Encoding UTF8
"services:`n  mongo:`n    environment:`n      MONGO_URL: mongodb://root:$($fake.MongoPass)@mongo:27017" | Set-Content (Join-Path $tmp 'docker-compose.yml') -Encoding UTF8
"2026-10-08 12:00:01 calling provider with key $($fake.LoggedKey)`n2026-10-08 12:00:02 ok" | Set-Content (Join-Path $tmp 'output\trainer.log') -Encoding UTF8
$env:FAKE_SERVICE_TOKEN = $fake.EnvVarToken

$script:DoctorRepoDir = $tmp
. (Join-Path $PSScriptRoot '..\Tools.ps1')

$failures = 0
function Test-Case([string]$Name, [string]$Tool, $ToolArgs, [string[]]$MustContain = @(), [hashtable]$Secrets = $fake) {
    $out = Invoke-DoctorTool $Tool ([pscustomobject]$ToolArgs)
    $leaks = @($Secrets.GetEnumerator() | Where-Object { $_.Value -and $out.Contains($_.Value) } | ForEach-Object { $_.Key })
    $missing = @($MustContain | Where-Object { -not $out.Contains($_) })
    if ($leaks -or $missing) {
        $script:failures++
        Write-Host "FAIL  $Name" -ForegroundColor Red
        if ($leaks) { Write-Host "      leaked: $($leaks -join ', ')" }
        if ($missing) { Write-Host "      expected to see: $($missing -join ' | ')" }
        if ($Secrets -eq $fake) { Write-Host ("      output: " + ($out.Substring(0, [Math]::Min(300, $out.Length)) -replace "`r?`n", ' / ')) }
    } else { Write-Host "ok    $Name" }
}

try {
    Write-Host "Planted fake secrets in $tmp"
    Test-Case 'read_file .env is masked, keeps non-secrets' 'read_file' @{ path = '.env' } @('SMTP_PORT=587', 'DECISION_TOKEN_TTL_SECONDS=86400', 'ANTHROPIC_API_KEY=<secret, ', 'OPENAI_API_KEY=', 'mongodb://root:<secret')
    Test-Case 'read_file absolute .env path' 'read_file' @{ path = (Join-Path $tmp '.env') } @('<secret, ')
    Test-Case 'read_file .env.example is readable' 'read_file' @{ path = '.env.example' } @('MONGO_ROOT_PASSWORD=')
    Test-Case 'read_file secrets\ is refused' 'read_file' @{ path = 'secrets\gmail.json' } @('holds secrets')
    Test-Case 'read_file compose file with URL password' 'read_file' @{ path = 'docker-compose.yml' } @('mongodb://root:<secret')
    Test-Case 'read_file log with a key in it' 'read_file' @{ path = 'output\trainer.log' } @('calling provider with key <secret>')
    Test-Case 'grep across the repo skips secret files' 'grep' @{ pattern = 'PASSWORD|refresh_token|key' } @('trainer.log')
    Test-Case 'grep on .env is refused' 'grep' @{ pattern = '.'; path = '.env' } @('holds secrets')
    Test-Case 'run_command Get-Content .env is refused' 'run_command' @{ command = 'Get-Content .env' } @('refused')
    Test-Case 'run_command Select-String on .\.env is refused' 'run_command' @{ command = 'Select-String -Path .\.env -Pattern KEY' } @('refused')
    Test-Case 'run_command reading secrets\ is refused' 'run_command' @{ command = 'Get-Content secrets\gmail.json' } @('refused')
    Test-Case 'run_command wildcard read is redacted' 'run_command' @{ command = 'Get-ChildItem -Force -File | ForEach-Object { Get-Content $_.FullName }' } @('<secret')
    Test-Case 'run_command Get-ChildItem env: is refused' 'run_command' @{ command = 'Get-ChildItem env:' } @('refused')
    Test-Case 'run_command gci Env:\ is refused' 'run_command' @{ command = 'gci Env:\' } @('refused')
    Test-Case 'run_command $env:FAKE_SERVICE_TOKEN is refused' 'run_command' @{ command = '$env:FAKE_SERVICE_TOKEN' } @('refused')
    Test-Case 'run_command $env:USERNAME is allowed' 'run_command' @{ command = '$env:USERNAME' } @($env:USERNAME)
    Test-Case 'run_command GetEnvironmentVariable sees nothing' 'run_command' @{ command = "[Environment]::GetEnvironmentVariable('FAKE_SERVICE_TOKEN')" } @('[exit 0]')
    Test-Case 'run_command echoing a key-shaped string is redacted' 'run_command' @{ command = "Write-Output 'key $($fake.LoggedKey)'" } @('key <secret>')
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    Remove-Item Env:\FAKE_SERVICE_TOKEN -ErrorAction SilentlyContinue
}

if (-not $SkipRealRepo -and (Test-Path (Join-Path $realRepo '.env'))) {
    Write-Host "`nThis repo's real .env ($realRepo); values are compared, never printed"
    $script:DoctorRepoDir = $realRepo
    $script:SecretValues = $null
    $real = @{}
    foreach ($e in Get-EnvFileEntries (Join-Path $realRepo '.env')) {
        if ($e.Name -and (Test-SecretName $e.Name) -and $e.Value.Length -ge 6) { $real[$e.Name] = $e.Value }
    }
    Write-Host "  $($real.Count) secret-named values to watch for"
    Test-Case 'real: read_file .env' 'read_file' @{ path = '.env' } @('<secret, ') $real
    Test-Case 'real: docker compose config' 'run_command' @{ command = 'docker compose config' } @() $real
    $ids = @(& docker ps -q 2>$null)
    if ($ids) {
        Test-Case "real: docker inspect of $($ids.Count) running containers" 'run_command' @{ command = "docker inspect $($ids -join ' ')"; timeout_seconds = 120 } @() $real
        Test-Case 'real: compose_ps' 'compose_ps' @{} @() $real
        Test-Case 'real: sim-controller logs' 'service_logs' @{ service = 'sim-controller'; lines = 400 } @() $real
    } else { Write-Host '  (no running containers; docker inspect check skipped)' }
}

Write-Host ''
if ($failures) { Write-Host "$failures check(s) failed." -ForegroundColor Red; exit 1 }
Write-Host 'All secret-guard checks passed.' -ForegroundColor Green
