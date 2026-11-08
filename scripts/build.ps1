# Builds installzero for this Windows machine into dist\installzero.exe with the
# release flags (see build-binaries.sh): a plain `go build` is quarantined by
# Defender's machine-learning heuristics from time to time.
param([string]$Version = 'dev')
$ErrorActionPreference = 'Stop'
Push-Location (Split-Path $PSScriptRoot)
try {
    $env:CGO_ENABLED = '0'
    go build -trimpath -ldflags "-s -w -buildid= -X github.com/InstallZero/installzero/internal/cli.Version=$Version" -o dist\installzero.exe ./cmd/installzero
    if ($LASTEXITCODE) { throw "go build failed ($LASTEXITCODE)" }
    Write-Host "built dist\installzero.exe ($Version)"
} finally { Pop-Location }
