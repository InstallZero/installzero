<#
.SYNOPSIS
    Fills in the expected outputs of the fixture files from the PowerShell code.

.DESCRIPTION
    Each fixture file holds cases: the inputs of one PowerShell function and
    what it returns for them. This script runs the functions on the inputs and
    rewrites the outputs, so after a deliberate behaviour change the fixtures
    follow it (review the diff). The Go port's tests read the same files.

    A string input given as a list is its pieces joined; fixtures use that
    for fake keys, so no file holds a whole one.
.PARAMETER Check
    Compare instead of write: exit code 1 when a file's outputs differ from
    what the code returns now.
#>
[CmdletBinding()]
param([switch]$Check)

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot
. (Join-Path $root 'doctor\Llm.ps1')
. (Join-Path $root 'doctor\Tools.ps1')
. (Join-Path $root 'agent\InstallMd.ps1')
. (Join-Path $root 'agent\LinuxTarget.ps1')
. (Join-Path $root 'agent\AgentLoop.ps1')

# Protect-Secrets reads the machine's secret values through this; fixtures give them per case.
$script:FixtureSecretValues = @()
function Get-SecretValues { $script:FixtureSecretValues }

function Join-Text($t) { if ($t -is [array]) { -join $t } else { [string]$t } }

# $Length characters of "text <n> " repeated: message bodies for the loop's trimming.
function New-FixtureText([int]$N, [int]$Length) { ("text $N " * ([math]::Ceiling($Length / 6) + 1)).Substring(0, $Length) }

function ConvertTo-FixtureJson($v) {
    if ($null -eq $v) { return 'null' }
    if ($v -is [string] -or $v -is [char]) {
        $sb = [Text.StringBuilder]::new('"')
        foreach ($ch in ([string]$v).ToCharArray()) {
            switch ($ch) {
                '"' { [void]$sb.Append('\"') } '\' { [void]$sb.Append('\\') }
                "`n" { [void]$sb.Append('\n') } "`r" { [void]$sb.Append('\r') } "`t" { [void]$sb.Append('\t') }
                "`b" { [void]$sb.Append('\b') } "`f" { [void]$sb.Append('\f') }
                default { if ([int]$ch -lt 0x20) { [void]$sb.Append(('\u{0:x4}' -f [int]$ch)) } else { [void]$sb.Append($ch) } }
            }
        }
        return $sb.Append('"').ToString()
    }
    if ($v -is [bool]) { return $(if ($v) { 'true' } else { 'false' }) }
    if ($v -is [int] -or $v -is [long] -or $v -is [double] -or $v -is [decimal]) { return ([IFormattable]$v).ToString($null, [Globalization.CultureInfo]::InvariantCulture) }
    if ($v -is [Collections.IDictionary]) {
        return '{' + (@(foreach ($k in $v.Keys) { (ConvertTo-FixtureJson "$k") + ': ' + (ConvertTo-FixtureJson $v[$k]) }) -join ', ') + '}'
    }
    if ($v -is [Management.Automation.PSCustomObject]) {
        return '{' + (@(foreach ($p in $v.PSObject.Properties) { (ConvertTo-FixtureJson $p.Name) + ': ' + (ConvertTo-FixtureJson $p.Value) }) -join ', ') + '}'
    }
    if ($v -is [Collections.IEnumerable]) { return '[' + (@(foreach ($x in $v) { ConvertTo-FixtureJson $x }) -join ', ') + ']' }
    throw "cannot write $($v.GetType()) as JSON"
}

$tmp = Join-Path ([IO.Path]::GetTempPath()) "iz-fixture-$PID.env"
$docsDir = Join-Path $PSScriptRoot 'installmd\docs'
$docCache = @{}
function Read-FixtureDoc([string]$File) {
    if (-not $docCache[$File]) { $docCache[$File] = Read-InstallMd (Join-Path $docsDir $File) }
    $docCache[$File]
}
function Select-BlockIndex($Step, [string]$Kind, $Machine) {
    $b = Select-StepBlock $Step $Kind $Machine
    if (-not $b) { return $null }
    for ($i = 0; $i -lt $Step.Blocks.Count; $i++) { if ([object]::ReferenceEquals($Step.Blocks[$i], $b)) { return $i } }
}

# File, input keys, and the outputs for one case.
$files = @(
    @('readonly\bash.json', @('code'), { param($c) [ordered]@{ reason = Test-BashReadOnly $c.code } }),
    @('readonly\bash-split.json', @('code'), { param($c) [ordered]@{ commands = @(Split-BashCommands $c.code) } }),
    @('readonly\powershell.json', @('command'), { param($c) [ordered]@{ reason = Test-ReadOnlyCommand $c.command } }),
    @('secrets\redact.json', @('text', 'values'), {
        param($c)
        $script:FixtureSecretValues = @($c.values | Sort-Object { $_.value.Length } -Descending | ForEach-Object { [pscustomobject]@{ Value = $_.value; Name = $_.name } })
        [ordered]@{ redacted = Protect-Secrets (Join-Text $c.text) }
    }),
    @('secrets\paths.json', @('path'), { param($c) [ordered]@{ secret = [bool](Test-SecretPath $c.path); env_file = $c.path -match $script:EnvFileRegex } }),
    @('secrets\names.json', @('name'), { param($c) [ordered]@{ secret = [bool](Test-SecretName $c.name) } }),
    @('secrets\linux.json', @('text'), { param($c) [ordered]@{ redacted = Protect-LinuxSecrets $c.text } }),
    @('secrets\envfile.json', @('content'), {
        param($c)
        [IO.File]::WriteAllText($tmp, $c.content, [Text.UTF8Encoding]::new($false))
        [ordered]@{
            entries = @(Get-EnvFileEntries $tmp | ForEach-Object { [ordered]@{ name = $_.Name; value = $_.Value; line = $_.Line } })
            masked  = (Get-MaskedEnvFile $tmp).Replace($tmp, '.env')
        }
    }),
    @('loops\repeat.json', @('text'), { param($c) [ordered]@{ unit = Find-RepeatLoop $c.text } }),
    @('loops\template.json', @('text'), { param($c) [ordered]@{ line = Find-TemplateLoop $c.text } }),
    @('loops\salvage.json', @('partial'), { param($c) [ordered]@{ content = Get-LoopSalvage $c.partial } }),
    @('loops\compress.json', @('messages', 'max_chars', 'keep'), {
        param($c)
        $i = 0
        $ms = @(foreach ($m in $c.messages) {
            $i++
            $h = @{ role = $m.role; content = $(if ($null -eq $m.content) { $null } else { New-FixtureText $i $m.content }) }
            if ($m.args) { $j = 0; $h.tool_calls = @(foreach ($n in $m.args) { $j++; @{ id = "c$i-$j"; type = 'function'; function = @{ name = 'x'; arguments = (New-FixtureText ($i * 10 + $j) $n) } } }) }
            $h
        })
        $before = @($ms | ForEach-Object { [ordered]@{ content = $_.content; args = @($_.tool_calls | Where-Object { $_ } | ForEach-Object { $_.function.arguments }) } })
        Compress-AgentMessages $ms $c.max_chars $c.keep
        $k = 0
        [ordered]@{ changed = @(foreach ($m in $ms) {
            $b = $before[$k]; $k++
            $args = @($m.tool_calls | Where-Object { $_ } | ForEach-Object { $_.function.arguments })
            $argsChanged = @(for ($x = 0; $x -lt $args.Count; $x++) { if ($args[$x] -cne $b.args[$x]) { $args[$x] } else { $null } })
            if ($m.content -cne $b.content -or @($argsChanged | Where-Object { $_ }).Count) { [ordered]@{ index = $k - 1; content = $(if ($m.content -cne $b.content) { $m.content } else { $null }); args = $argsChanged } }
        }) }
    }),
    @('installmd\docs.json', @('file'), {
        param($c)
        $d = Read-FixtureDoc $c.file
        [ordered]@{
            meta     = $d.Meta
            steps    = @(foreach ($s in $d.Steps) {
                [ordered]@{
                    number = $s.Number; id = $s.Id; title = $s.Title; why = $s.Why; line = $s.Line; requires = $s.Requires
                    blocks = @(foreach ($b in $s.Blocks) { [ordered]@{ kind = $b.Kind; shell = $b.Shell; when = $b.When; code = $b.Code } })
                }
            })
            prose    = $d.Prose
            problems = @(Test-InstallMd $d)
        }
    }),
    @('installmd\select.json', @('file', 'machine'), {
        param($c)
        [ordered]@{
            steps = @(foreach ($s in (Read-FixtureDoc $c.file).Steps) {
                [ordered]@{
                    id      = $s.Id
                    unmet   = @(Get-UnmetRequirements $s $c.machine)
                    detect  = Select-BlockIndex $s 'detect' $c.machine
                    install = Select-BlockIndex $s 'install' $c.machine
                    verify  = Select-BlockIndex $s 'verify' $c.machine
                }
            })
        }
    }),
    @('installmd\conditions.json', @('key', 'want', 'machine'), { param($c) [ordered]@{ met = [bool](Test-Condition $c.key $c.want $c.machine) } }),
    @('installmd\placeholders.json', @('code', 'values'), {
        param($c)
        $h = @{}; foreach ($p in $c.values.PSObject.Properties) { $h[$p.Name] = $p.Value }
        [ordered]@{ expanded = Expand-Placeholders $c.code $h }
    })
)

$stale = 0
try {
    foreach ($f in $files) {
        $path = Join-Path $PSScriptRoot $f[0]
        $old = [IO.File]::ReadAllText($path)
        $doc = $old | ConvertFrom-Json
        $lines = foreach ($c in $doc.cases) {
            $case = [ordered]@{}
            foreach ($k in $f[1]) { $case[$k] = $c.$k }
            $out = & $f[2] $c
            foreach ($k in $out.Keys) { $case[$k] = $out[$k] }
            '    ' + (ConvertTo-FixtureJson $case)
        }
        $new = "{`n  `"description`": $(ConvertTo-FixtureJson $doc.description),`n  `"cases`": [`n" + ($lines -join ",`n") + "`n  ]`n}`n"
        if ($new -ceq $old) { Write-Host "  same     $($f[0]) ($(@($doc.cases).Count) cases)"; continue }
        if ($Check) {
            $oldLines = $old -split "`n"; $newLines = $new -split "`n"
            $n = @(for ($i = 0; $i -lt [math]::Max($oldLines.Count, $newLines.Count); $i++) { if ($oldLines[$i] -cne $newLines[$i]) { $i } }).Count
            Write-Host "  DIFFERS  $($f[0]) ($n lines)" -ForegroundColor Red
            $stale++
        } else {
            [IO.File]::WriteAllText($path, $new, [Text.UTF8Encoding]::new($false))
            Write-Host "  updated  $($f[0]) ($(@($doc.cases).Count) cases)"
        }
    }
} finally { Remove-Item $tmp -ErrorAction SilentlyContinue }
if ($Check -and $stale) { Write-Host "$stale fixture file(s) differ from the PowerShell code; run fixtures\Update-Fixtures.ps1 and review the diff." -ForegroundColor Red; exit 1 }
