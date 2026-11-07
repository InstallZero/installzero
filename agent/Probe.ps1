<#
.SYNOPSIS
    InstallZero probe: works backwards from a running install on this
    machine to an install.md the InstallZero agent can replay elsewhere.

.DESCRIPTION
    Read-only. The model inventories what runs (processes, listening ports,
    containers and compose projects, git repos, Python and Node
    environments, installed software, GPU stack, startup entries), works out
    how it was installed, and writes an install.md (format in
    INSTALL-MD.md). Each time it writes a draft, the steps' detect and
    verify checks are run here, where the project is installed, so they
    should pass; failures go back to the model to fix (checks that are not
    read-only are reported as untested, not run).

    Refine a draft with what a replay on a clean machine showed:
    -Draft <install.md> -AgentReport <Agent.ps1 report.json>.

.PARAMETER Target
    Where to start: a folder (the project's checkout), a URL or port it
    serves on, or a name. Without it the probe looks for the most likely
    project among what runs.

.PARAMETER NoDocs
    Kept for compatibility: docs, install scripts, installer logs and shell
    history are always hidden, so the procedure has to come from what runs.

.PARAMETER HidePaths
    Test-harness folders and files to hide as well (staging folder,
    installer state, results).

.PARAMETER Wsl
    Probe a project installed in this WSL distro instead of on Windows: every
    tool runs inside it (bash, as root), and -Target is a path there.

.PARAMETER GuidanceFile
    JSON {id, text}: a lesson set from the eval console (habits learned from
    earlier misses), added to the system prompt. The prompt version becomes
    probe-N+<id>.

.EXAMPLE
    .\Probe.ps1 -Target C:\Users\me\ComfyUI -Out .\comfyui.install.md
#>
[CmdletBinding()]
param(
    [string]$Target = '',
    [string]$Out = '',
    [string]$Draft = '',
    [string]$AgentReport = '',
    [switch]$NoDocs,
    [string]$Model = '',
    [ValidateSet('', 'cuda', 'vulkan', 'cpu')][string]$Backend = '',
    [string]$LlmUrl = '',
    [string]$Remote = '',
    # Test-harness folders and files the probe must not see (staging,
    # installer state, results); environment variables are expanded.
    [string[]]$HidePaths = @(),
    [string]$Wsl = '',
    # Largest context for a local model; less is used when GPU memory is short.
    [int]$Context = 32768,
    [Alias('MaxSteps')][int]$MaxTurns = 40,
    [int]$TimeoutMinutes = 30,
    [string]$ResultFile = '',
    [string]$GuidanceFile = '',
    # Sampling seed sent with every request to a llama-server (0: none).
    [int]$Seed = 0,
    # Write the tool definitions offered to the model as JSON and stop (the
    # SFT export pairs them with transcripts, which do not record them).
    [string]$ToolsOut = '',
    # Write the system prompt, first user message and tools as JSON and stop
    # (the Go engine is checked against this).
    [string]$PromptOut = '',
    # Run the tool calls in this JSON file ([{name, arguments}]) and write
    # their outputs to <file>.out.json, then stop (the Go engine is checked
    # against this).
    [string]$ToolCalls = '',
    [switch]$Quiet
)

$ErrorActionPreference = 'Stop'
$script:AgentQuiet = [bool]$Quiet
$doctorDir = Join-Path $PSScriptRoot '..\doctor'
. (Join-Path $doctorDir 'Llm.ps1')
. (Join-Path $doctorDir 'Tools.ps1')
. (Join-Path $PSScriptRoot 'InstallMd.ps1')
. (Join-Path $PSScriptRoot 'Policy.ps1')
. (Join-Path $PSScriptRoot 'AgentLoop.ps1')
. (Join-Path $PSScriptRoot 'LinuxTarget.ps1')
$linux = [bool]$Wsl

# The probe recovers the procedure from what runs, blind to anything that
# describes the install: docs, install and uninstall scripts, installer state
# and logs, shell history, InstallZero's own files (including the expected
# answers), and the -HidePaths of the test harness.
$script:HiddenPathRegex += '|scripts(\\|/)install(\\|/)agent|installzero(\\|/)(probe|agent|eval)' +
    '|(?i)(^|\\|/)(README|INSTALL|SETUP|GETTING[-_]?STARTED|QUICKSTART|CONTRIBUTING|CHANGELOG|HISTORY)[^\\/]*$|(?i)(\\|/)docs?(\\|/)|(?i)\.(md|markdown|rst|adoc)$' +
    '|(?i)(^|\\|/)(un)?install[^\\/]*\.(ps1|psm1|sh|bash|bat|cmd|py)$|(?i)(^|\\|/)(setup|bootstrap)\.(ps1|sh|bash|bat|cmd)$|(?i)(\\|/)scripts(\\|/)install(\\|/)|(?i)(\\|/)installers?(\\|/)' +
    '|(?i)PSReadLine(\\|/)ConsoleHost_history|(?i)(^|\\|/)\.(bash|zsh|sh|python|node_repl|psql|mysql)_history$|(?i)(\\|/)Microsoft(\\|/)Windows(\\|/)Recent(\\|/)'
$hidden = @($HidePaths | ForEach-Object { $_ -split ',' } | ForEach-Object { $_.Trim().Trim("'").Trim('"') } | Where-Object { $_ } | ForEach-Object { [IO.Path]::GetFullPath([Environment]::ExpandEnvironmentVariables($_)).TrimEnd('\') })
foreach ($h in $hidden) { $script:HiddenPathRegex += '|(?i)' + [regex]::Escape($h) + '(\\|/|$)' }
# The WSL harness's own settings, and package-manager logs (installer logs).
if ($linux) { $script:HiddenPathRegex += '|/etc/wsl\.conf|/var/log/(apt|dpkg|installer)\b|\.(wget-hsts|lesshst)$' }
$targetDir = if ($linux) { $null } elseif ($Target -and (Test-Path -LiteralPath $Target -PathType Container)) { (Resolve-Path -LiteralPath $Target).Path } else { $null }
$linuxDir = if ($linux -and $Target -match '^/') { $Target.TrimEnd('/') } else { $null }
$script:DoctorRepoDir = if ($targetDir) { $targetDir } elseif ($linux) { $script:InstallZeroRoot } else { $env:USERPROFILE }
if (-not $Out) { $Out = Join-Path (Get-Location) "probe-$(Get-Date -Format 'yyyyMMdd-HHmmss').install.md" }
$Out = [IO.Path]::GetFullPath($Out)
# The draft being written is the one hidden-looking file the model may read.
function Test-HiddenPath([string]$Path) { $Path -ne $script:Out -and $Path -match $script:HiddenPathRegex }
# A hidden path that shows up in what runs (a startup shortcut's target, a
# git remote) is evidence; only file contents are hidden, by the content
# guard. Lines naming InstallZero's own files are still dropped.
$script:OwnFilesRegex = '(?i)' + $script:InstallZeroPathRegex + '|scripts(\\|/)install(\\|/)(agent|doctor)|installzero(\\|/)probe|doctor-assets|eval-results\.jsonl'
function Remove-HiddenLines([string]$Text) { (($Text -split "`r?`n") | Where-Object { $_ -notmatch $script:OwnFilesRegex }) -join "`n" }

# Content guard: lines that occur only in hidden files are withheld from every
# tool output, however the model reached them (a wildcard, a copy, git
# history). Lines shared with visible files, and short lines, stay.
function Get-ProbeTextFiles([string]$Root, [int]$Max = 20000) {
    if (-not (Test-Path -LiteralPath $Root)) { return @() }
    if (Test-Path -LiteralPath $Root -PathType Leaf) { return @(Get-Item -LiteralPath $Root) }
    @(Get-ChildItem -LiteralPath $Root -Recurse -File -Force -ErrorAction SilentlyContinue |
        Where-Object { $_.FullName -notmatch '\\(\.git|node_modules|Library|Temp|Logs|obj|bin|__pycache__|\.venv|venv|site-packages|UnityBinary)\\' -and $_.Length -lt 512KB -and
                       ($_.Extension -match '^\.(ps1|psm1|psd1|sh|bash|bat|cmd|py|md|markdown|rst|txt|json|ya?ml|toml|ini|cfg|conf|js|ts|cs|xml|log)$' -or $_.Name -match '^(Dockerfile|Makefile)') -and -not (Test-SecretPath $_.FullName) } |
        Select-Object -First $Max)
}
# Running services keep their logs open for writing; share access, and skip
# files that still cannot be read.
function Read-SharedLines([string]$Path) {
    try {
        $sr = [IO.StreamReader]::new([IO.FileStream]::new($Path, 'Open', 'Read', 'ReadWrite, Delete'))
        try { while ($null -ne ($l = $sr.ReadLine())) { $l } } finally { $sr.Dispose() }
    } catch { }
}
$script:LeakLines = [Collections.Generic.HashSet[string]]::new()
$script:Withheld = 0; $script:HiddenAttempts = 0
$visibleLines = [Collections.Generic.HashSet[string]]::new()
$hiddenFiles = [Collections.Generic.List[object]]::new()
if ($targetDir) {
    foreach ($f in (Get-ProbeTextFiles $targetDir)) {
        if (Test-HiddenPath $f.FullName) { $hiddenFiles.Add($f) }
        else { foreach ($l in (Read-SharedLines $f.FullName)) { $t = $l.Trim(); if ($t.Length -ge 30) { [void]$visibleLines.Add($t) } } }
    }
}
if ($linuxDir) {
    $vis = Invoke-WslBash $Wsl "find '$linuxDir' -maxdepth 4 -type f -size -512k \( -name '*.yml' -o -name '*.yaml' -o -name '*.json' -o -name '*.conf' -o -name '*.env' -o -name '*.toml' -o -name '*.ini' -o -name '*.cfg' -o -name '*.py' -o -name '*.sh' -o -name '*.txt' -o -name 'Dockerfile' -o -name 'env' \) 2>/dev/null | head -2000 | while read f; do cat `"`$f`" 2>/dev/null; done" 120
    foreach ($l in ($vis.Out -split "`n")) { $t = $l.Trim(); if ($t.Length -ge 30) { [void]$visibleLines.Add($t) } }
}
foreach ($h in $hidden) { foreach ($f in (Get-ProbeTextFiles $h 5000)) { $hiddenFiles.Add($f) } }
$psHistory = Join-Path $env:APPDATA 'Microsoft\Windows\PowerShell\PSReadLine\ConsoleHost_history.txt'
if (Test-Path $psHistory) { $hiddenFiles.Add((Get-Item $psHistory)) }
foreach ($f in $hiddenFiles) {
    foreach ($l in (Read-SharedLines $f.FullName)) { $t = $l.Trim(); if ($t.Length -ge 30 -and -not $visibleLines.Contains($t)) { [void]$script:LeakLines.Add($t) } }
}
function Remove-LeakedLines([string]$Text) {
    if (-not $script:LeakLines.Count -or -not $Text) { return $Text }
    $n = 0
    $kept = foreach ($l in ($Text -split "`r?`n")) {
        $t = ($l -replace '^\s*\d+\|\s?', '').Trim()
        if ($t.Length -ge 30 -and $script:LeakLines.Contains($t)) { $n++ } else { $l }
    }
    if (-not $n) { return $Text }
    $script:Withheld += $n
    # Mostly hidden content means a hidden file was dumped: its short lines go too.
    $left = @($kept | Where-Object { $_.Trim() -and $_ -notmatch '^\[exit \d+\]$' }).Count
    if ($n -ge 5 -and $n -ge $left) { return "[output withheld: it is the content of install files, which the probe does not see]" }
    (@($kept) -join "`n") + "`n[$n line(s) withheld: they come from install files, which the probe does not see]"
}

# ---- inventory (read-only) -------------------------------------------------------

function Format-Table2($Rows, [string[]]$Cols) { ($Rows | Format-Table $Cols -AutoSize | Out-String -Width 400).Trim() }

function Get-Inventory([string]$Area, [string]$Filter, [string]$Path) {
    $f = if ($Filter) { $Filter } else { '.' }
    switch ($Area) {
        'software' {
            $keys = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\*', 'HKCU:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\*'
            $rows = Get-ItemProperty $keys -ErrorAction SilentlyContinue | Where-Object { $_.DisplayName -and $_.DisplayName -notmatch '^(Update for|Security Update|Microsoft Visual C\+\+ 20\d\d .*Additional|Microsoft \.NET .*(Host|Targeting))' -and $_.DisplayName -match $f } |
                Sort-Object DisplayName -Unique | Select-Object DisplayName, DisplayVersion, Publisher, @{ n = 'InstallDate'; e = { $_.InstallDate } }
            return "Installed programs$(if ($Filter) { " matching '$Filter'" }) ($(@($rows).Count)):`n" + (Format-Table2 $rows 'DisplayName', 'DisplayVersion', 'Publisher', 'InstallDate')
        }
        'containers' {
            # Windows PowerShell drops inner quotes from native arguments, so
            # --format '{{.Label "x"}}' cannot be passed; read inspect JSON.
            $ids = Invoke-Docker @('ps', '-a', '-q')
            if ($ids.Code -ne 0) { return "docker is not available: $($ids.Out)" }
            $idList = @($ids.Out -split "`r?`n" | Where-Object { $_.Trim() })
            $info = if ($idList) { (Invoke-Docker (@('inspect') + $idList)).Out | ConvertFrom-Json } else { @() }
            $rows = @($info | ForEach-Object {
                $l = $_.Config.Labels
                $ports = @($_.NetworkSettings.Ports.PSObject.Properties | Where-Object { $_.Value } | ForEach-Object { "$($_.Value[0].HostPort)->$($_.Name)" }) -join ', '
                [pscustomobject]@{ Name = $_.Name.TrimStart('/'); Image = $_.Config.Image; Status = $_.State.Status; Ports = $ports
                    Project = $l.'com.docker.compose.project'; Service = $l.'com.docker.compose.service'; WorkingDir = $l.'com.docker.compose.project.working_dir'; ComposeFiles = $l.'com.docker.compose.project.config_files' }
            } | Where-Object { "$($_.Name) $($_.Image) $($_.Project)" -match $f })
            $img = Invoke-Docker @('images', '--format', '{{.Repository}}:{{.Tag}}|{{.Size}}|{{.CreatedSince}}')
            return "Containers ($($rows.Count)):`n" + (Format-Table2 $rows 'Name', 'Image', 'Status', 'Ports', 'Project', 'Service') + "`n`nCompose projects (working dir and files):`n" + (($rows | Where-Object Project | Sort-Object Project -Unique | ForEach-Object { "  $($_.Project): $($_.WorkingDir) [$($_.ComposeFiles)]" }) -join "`n") + "`n`nImages:`n" + (($img.Out -split "`r?`n" | Where-Object { $_ -match $f } | Select-Object -First 60) -join "`n")
        }
        'processes' {
            $me = (Get-Process -Id $PID).SessionId
            $skip = '^(svchost|RuntimeBroker|conhost|dllhost|sihost|ctfmon|explorer|SearchHost|StartMenuExperienceHost|ShellExperienceHost|TextInputHost|smartscreen|backgroundTaskHost|taskhostw|SecurityHealthSystray|msedgewebview2|WidgetService|Widgets|PhoneExperienceHost|CrossDeviceService|LockApp|dwm|fontdrvhost|winlogon|csrss|System|Idle|Registry|wininit|services|lsass|powershell|pwsh|WindowsTerminal|OpenConsole|cursor|Cursor|Code|msedge|chrome|firefox)\.exe$|^(System|Idle|Registry)$'
            $rows = Get-CimInstance Win32_Process | Where-Object { $_.SessionId -eq $me -and $_.Name -notmatch $skip -and "$($_.Name) $($_.CommandLine)" -match $f } |
                Select-Object ProcessId, Name, @{ n = 'CommandLine'; e = { Format-AgentBrief "$($_.CommandLine)" 300 } } | Select-Object -First 80
            return "Processes in this session$(if ($Filter) { " matching '$Filter'" }) ($(@($rows).Count), shells and browsers left out):`n" + (($rows | ForEach-Object { "$($_.ProcessId) $($_.Name): $($_.CommandLine)" }) -join "`n")
        }
        'ports' {
            $rows = Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue | Where-Object { $_.LocalPort -lt 49152 } | Sort-Object LocalPort -Unique |
                ForEach-Object { $p = Get-Process -Id $_.OwningProcess -ErrorAction SilentlyContinue; [pscustomobject]@{ Port = $_.LocalPort; Address = $_.LocalAddress; Pid = $_.OwningProcess; Process = $p.ProcessName; Path = $p.Path } } | Where-Object { "$($_.Port) $($_.Process) $($_.Path)" -match $f }
            return "Listening TCP ports:`n" + (Format-Table2 $rows 'Port', 'Address', 'Pid', 'Process', 'Path')
        }
        'python' {
            $lines = @("py launcher: $((& py -0p 2>&1) -join '; ')", "python on PATH: $((Get-Command python -All -ErrorAction SilentlyContinue).Source -join '; ')", "uv: $((Get-Command uv -ErrorAction SilentlyContinue).Source)")
            if (Get-Command conda -ErrorAction SilentlyContinue) { $lines += "conda envs:`n$((& conda env list 2>&1) -join "`n")" }
            $root = if ($Path) { $Path } elseif ($targetDir) { $targetDir } else { $null }
            if ($root) {
                $cfgs = @(Get-ChildItem $root -Filter pyvenv.cfg -Recurse -Depth 3 -ErrorAction SilentlyContinue | Select-Object -First 8)
                foreach ($c in $cfgs) {
                    $py = Join-Path $c.DirectoryName 'Scripts\python.exe'
                    $lines += "venv $($c.DirectoryName): $((Get-Content $c.FullName) -join '; ')"
                    if (Test-Path $py) { $lines += "  packages: $((& $py -m pip list --format freeze --disable-pip-version-check 2>$null | Where-Object { $_ -match $f } | Select-Object -First 80) -join ', ')" }
                }
                if (-not $cfgs) { $lines += "no virtual environment (pyvenv.cfg) under $root" }
            }
            return $lines -join "`n"
        }
        'node' { return "node $((& node -v 2>&1)); npm $((& npm -v 2>&1)); global packages: $((& npm ls -g --depth 0 2>&1 | Select-Object -Skip 1) -join ', ')" }
        'git' {
            $root = if ($Path) { $Path } elseif ($targetDir) { $targetDir } else { $env:USERPROFILE }
            $repos = @(Get-ChildItem $root -Directory -Filter .git -Recurse -Depth 3 -Force -ErrorAction SilentlyContinue | Select-Object -First 15)
            if (Test-Path (Join-Path $root '.git')) { $repos = @(Get-Item (Join-Path $root '.git') -Force) + $repos }
            return "Git repositories under $($root):`n" + (($repos | Sort-Object FullName -Unique | ForEach-Object {
                $d = $_.Parent.FullName
                "  $d`n    remote: $((& git -C $d remote get-url origin 2>&1))`n    branch: $((& git -C $d rev-parse --abbrev-ref HEAD 2>&1)) at $((& git -C $d log -1 --format='%h %cs %s' 2>&1))`n    local changes: $(@(& git -C $d status --porcelain 2>$null).Count) files; submodules: $(@(& git -C $d submodule status 2>$null).Count)"
            }) -join "`n")
        }
        'gpu' { return "nvidia-smi:`n$((& nvidia-smi 2>&1 | Select-Object -First 25) -join "`n")`nnvcc: $((& nvcc --version 2>&1 | Select-Object -Last 2) -join ' ')`nCUDA_PATH: $env:CUDA_PATH" }
        'wsl' { return ((& wsl.exe -l -v 2>&1) -replace "`0", '') -join "`n" }
        'env' {
            $rows = foreach ($scope in 'User', 'Machine') {
                $vars = [Environment]::GetEnvironmentVariables($scope)
                foreach ($k in $vars.Keys) { if ($k -ne 'Path' -and "$k" -match $f) { "$scope $k = $(if (Test-SecretName $k) { '<secret, set>' } else { Format-AgentBrief "$($vars[$k])" 200 })" } }
            }
            $path = ([Environment]::GetEnvironmentVariable('Path', 'User') -split ';' | Where-Object { $_ }) -join "`n  "
            return "Environment variables (User and Machine scope):`n$(($rows | Sort-Object) -join "`n")`n`nUser PATH:`n  $path"
        }
        'startup' {
            $lnk = @(Get-ChildItem ([Environment]::GetFolderPath('Startup')) -ErrorAction SilentlyContinue | ForEach-Object { $s = (New-Object -ComObject WScript.Shell).CreateShortcut($_.FullName); "  $($_.Name): $($s.TargetPath) $($s.Arguments)" })
            $run = Get-ItemProperty 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Run' -ErrorAction SilentlyContinue
            $runs = @($run.PSObject.Properties | Where-Object { $_.Name -notmatch '^PS' } | ForEach-Object { "  $($_.Name): $($_.Value)" })
            $tasks = @(Get-ScheduledTask -ErrorAction SilentlyContinue | Where-Object { $_.TaskPath -notmatch '^\\Microsoft\\' -and $_.State -ne 'Disabled' } | ForEach-Object { "  $($_.TaskPath)$($_.TaskName): $(($_.Actions | ForEach-Object { "$($_.Execute) $($_.Arguments)" }) -join '; ')" })
            return "Startup folder:`n$($lnk -join "`n")`nRun key:`n$($runs -join "`n")`nScheduled tasks (not Microsoft):`n$($tasks -join "`n")"
        }
        default { return "error: unknown area '$Area'" }
    }
}

# Checks the model may run against the live install: read-only commands and
# version queries. Anything else is reported untested instead of run.
function Test-ProbeCheck([string]$Code, [string]$Shell) {
    if ($linux) {
        if ($Shell -notin '', 'bash', 'sh') { return @{ Ok = $false; Why = "$Shell blocks are not run on this Linux machine" } }
        $why = Test-BashReadOnly $Code
        return $(if ($why) { @{ Ok = $false; Why = "not read-only ($why), so not run here (it may still be right)" } } else { @{ Ok = $true } })
    }
    if ($Shell -notin '', 'powershell', 'pwsh') { return @{ Ok = $false; Why = "$Shell blocks are not run on this Windows PC" } }
    if ((Get-CommandTier $Code $Shell @()).Tier -eq 'read') { return @{ Ok = $true } }
    $lines = @($Code -split "`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ -and $_ -notmatch '^#' })
    $version = '^(&\s*)?("[^"]+"|''[^'']+''|[\w.\\/:-]+)(\s+-\d+(\.\d+)?)?\s+(--version|-V|version|--help)\s*$'
    if ($lines -and -not @($lines | Where-Object { $_ -notmatch $version -and (Test-ReadOnlyCommand $_) }).Count) { return @{ Ok = $true } }
    @{ Ok = $false; Why = 'not read-only, so not run here (it may still be right)' }
}

function Invoke-DraftCheck([string]$Content) {
    $dir = Split-Path $script:Out
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Force $dir | Out-Null }
    [IO.File]::WriteAllText($script:Out, $Content.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
    $doc = Read-InstallMd $script:Out
    $problems = @(Test-InstallMd $doc)
    $machine = $script:Machine
    $name = if ($doc.Meta['name']) { $doc.Meta['name'] } else { 'project' }
    $ph = if ($linux) { @{ INSTALL_DIR = $(if ($linuxDir) { ($linuxDir -replace '/[^/]+$', '') -replace '^$', '/' } else { '/opt' }); REPO_DIR = $(if ($linuxDir) { $linuxDir } else { "/opt/$name" }) } }
          else { @{ INSTALL_DIR = $(if ($targetDir) { Split-Path $targetDir } else { $env:USERPROFILE }); REPO_DIR = $(if ($targetDir) { $targetDir } else { Join-Path $env:USERPROFILE $name }) } }
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add("Saved $($script:Out): $($doc.Steps.Count) steps.$(if ($problems) { " Format problems: $($problems -join '; ')" } else { ' Format OK.' })")
    $lines.Add("Checks run here, where the project is installed (detect and verify should both pass; {{INSTALL_DIR}}=$($ph.INSTALL_DIR), {{REPO_DIR}}=$($ph.REPO_DIR)):")
    $script:DraftStats = [ordered]@{ steps = $doc.Steps.Count; problems = $problems.Count; passed = 0; failed = 0; untested = 0 }
    foreach ($s in $doc.Steps) {
        foreach ($k in 'detect', 'verify') {
            $b = Select-StepBlock $s $k $machine
            if (-not $b) { if ($k -eq 'verify') { $lines.Add("  $($s.Id) $($k): no block for this machine") }; continue }
            $code = Expand-Placeholders $b.Code $ph
            $t = Test-ProbeCheck $code $b.Shell
            if (-not $t.Ok) { $script:DraftStats.untested++; $lines.Add("  $($s.Id) $($k): untested - $($t.Why)"); continue }
            $r = if ($linux) { Format-WslResult (Invoke-WslBash $Wsl $code 60 $ph.REPO_DIR) } else { Invoke-IsolatedPowerShell $code 60 $(if ($targetDir) { $targetDir } else { $env:USERPROFILE }) }
            $code0 = if ($r -match '^\[exit (\d+)\]') { [int]$Matches[1] } else { 1 }
            if ($code0 -eq 0) { $script:DraftStats.passed++ } else { $script:DraftStats.failed++ }
            $lines.Add("  $($s.Id) $($k): $(if ($code0 -eq 0) { 'PASS' } else { "FAIL (exit $code0)" }) - $(Format-AgentBrief (($r -split "`n" | Select-Object -Skip 1 | Where-Object { $_.Trim() }) -join ' ') 200)")
        }
    }
    # Later drafts can regress; keep the best one seen (format problems weigh most).
    $score = $script:DraftStats.passed - $script:DraftStats.failed - 5 * $script:DraftStats.problems + $script:DraftStats.steps
    if ($null -eq $script:BestDraft -or $score -ge $script:BestDraft.Score) { $script:BestDraft = @{ Score = $score; Content = $Content; Stats = $script:DraftStats } }
    $lines -join "`n"
}

# ---- tools and prompt ----------------------------------------------------------

$tools = if ($linux) { @('run_command', 'read_file', 'list_dir', 'grep', 'inventory' | ForEach-Object { Get-PromptTool "linux/$_" }) }
         else { @($script:DoctorToolSchemas | Where-Object { $_.function.name -notin 'finish', 'compose_ps', 'service_logs' }) + @(Get-PromptTool 'windows/inventory') }
$tools += @('test_check', 'write_install_md', 'finish' | ForEach-Object { Get-PromptTool "probe/$_" })
if ($ToolsOut) {
    [IO.File]::WriteAllText([IO.Path]::GetFullPath($ToolsOut), (ConvertTo-Json @($tools) -Depth 20), [Text.UTF8Encoding]::new($false))
    return
}

function ConvertTo-BashQuoted([string]$s) { "'" + $s.Replace("'", "'\''") + "'" }
function Invoke-LinuxProbeTool([string]$Name, $A, [string]$Code) {
    $cwd = if ($linuxDir) { $linuxDir } else { '/' }
    $path = "$($A.path)"
    if ($path -and $path -notmatch '^/') { $path = "$cwd/$path" }
    if ($Name -in 'read_file', 'list_dir', 'grep' -and $path -and (Test-HiddenPath $path)) { $script:HiddenAttempts++; return 'refused: that path is hidden: the probe works from what runs, not from install files or docs.' }
    if ($Name -in 'run_command', 'test_check' -and (Test-HiddenPath $Code)) { $script:HiddenAttempts++; return 'refused: that path is hidden: the probe works from what runs, not from install files or docs.' }
    $out = switch ($Name) {
        'inventory' {
            $t = Get-LinuxInventory $Wsl "$($A.area)" $path
            if ($A.filter) { $t = (($t -split "`n") | Where-Object { $_ -match "(?i)$($A.filter)" -or $_ -match ':$' }) -join "`n" }
            $t
        }
        'read_file' {
            if (Test-SecretPath $path) { 'refused: that file holds secrets.' }
            else {
                $start = [math]::Max(1, [int]("0$($A.start_line)")); $n = [math]::Min(400, $(if ($A.max_lines) { [int]$A.max_lines } else { 200 }))
                $r = Invoke-WslBash $Wsl "f=$(ConvertTo-BashQuoted $path); [ -f `"`$f`" ] || { echo `"not a file: `$f`"; exit 1; }; grep -Iq . `"`$f`" || [ ! -s `"`$f`" ] || { echo 'binary file'; exit 1; }; echo `"`$f (`$(wc -l < `"`$f`") lines)`"; awk -v s=$start -v e=$($start + $n - 1) 'NR>=s && NR<=e {printf `"%d|%s\n`", NR, `$0}' `"`$f`""
                $r.Out
            }
        }
        'list_dir' { (Invoke-WslBash $Wsl "ls -la --time-style=long-iso $(ConvertTo-BashQuoted $(if ($path) { $path } else { $cwd }))").Out }
        'grep' {
            $inc = if ($A.include) { "--include=$(ConvertTo-BashQuoted "$($A.include)")" } else { '' }
            $r = Invoke-WslBash $Wsl "grep -rnIE $inc --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=proc --exclude-dir=sys -e $(ConvertTo-BashQuoted "$($A.pattern)") $(ConvertTo-BashQuoted $(if ($path) { $path } else { $cwd })) 2>/dev/null | cut -c1-400 | head -300" 90
            $lines = @($r.Out -split "`n" | Where-Object { $_ -and -not (Test-HiddenPath ($_ -replace ':\d+:.*$', '')) })
            if ($lines) { $lines -join "`n" } else { 'no matches' }
        }
        { $_ -in 'run_command', 'test_check' } {
            $why = Test-BashReadOnly $Code
            if ($why) { "refused: $why. Only read-only commands run here." }
            else {
                $to = if ($Name -eq 'run_command' -and $A.timeout_seconds) { [math]::Min(180, [int]$A.timeout_seconds) } else { 60 }
                Format-WslResult (Invoke-WslBash $Wsl $Code $to $cwd)
            }
        }
        default { "error: unknown tool '$Name'" }
    }
    Protect-LinuxSecrets (Protect-Secrets (Remove-HiddenLines "$out"))
}

$gitContent = '(?i)\bgit\b[^|;\r\n]*\s(show|cat-file|diff|grep|archive|blame|whatchanged|format-patch|bundle|log\b[^|;\r\n]*\s(-p|--patch|-L|--follow)\b)'
$dispatch = {
    param($Name, $A)
    if ($Name -eq 'write_install_md') { return Invoke-DraftCheck "$($A.content)" }
    $code = if ($Name -eq 'run_command') { "$($A.command)" } elseif ($Name -eq 'test_check') { "$($A.code)" } else { '' }
    if ($code -match $gitContent) { $script:HiddenAttempts++; return 'refused: git commands that print file contents, diffs or patches are not available, since they would show the hidden install files. git remote, branch, status, rev-parse and log without -p work.' }
    if ($linux) { return Remove-LeakedLines (Invoke-LinuxProbeTool $Name $A $code) }
    $out = switch ($Name) {
        'inventory' { Protect-Secrets (Remove-HiddenLines (Get-Inventory "$($A.area)" "$($A.filter)" "$($A.path)")) }
        'test_check' {
            $t = Test-ProbeCheck $code 'powershell'
            if (-not $t.Ok) { "not run: $($t.Why)" }
            elseif (Test-HiddenPath $code) { 'refused: that path is hidden from the probe.' }
            else { Protect-Secrets (Remove-HiddenLines (Invoke-IsolatedPowerShell $code 60 $(if ($targetDir) { $targetDir } else { $env:USERPROFILE }))) }
        }
        default { Invoke-DoctorTool $Name $A }
    }
    $out = "$out"
    if ($out -match 'part of the doctor itself') {
        $script:HiddenAttempts++
        $out = $out -replace 'is part of the doctor itself, not the install', 'is hidden: the probe works from what runs, not from install files or docs' -replace 'that path is part of the doctor itself, not the install', 'that path is hidden from the probe'
    }
    Remove-LeakedLines $out
}

$spec = Get-Content (Join-Path $PSScriptRoot 'INSTALL-MD.md') -Raw
$machine = if ($linux) { Get-WslMachineProfile $Wsl } else { Get-MachineProfile }
$script:Machine = $machine
$system = Get-Prompt 'probe' @{
    os_word = $(if ($linux) { 'Linux' } else { 'Windows' }); linux = $linux; os_version = "$($machine.os_version)"; arch = "$($machine.arch)"; gpu = "$($machine.gpu)"; gpus = "$($machine.gpus)"
    driver = "$($machine.driver)"; ram_gb = "$($machine.ram_gb)"; half_turns = [math]::Floor($MaxTurns / 2); max_turns = $MaxTurns; spec = $spec
}
$promptVersion = 'probe-5'
if ($GuidanceFile) {
    $guidance = [IO.File]::ReadAllText($GuidanceFile) | ConvertFrom-Json
    if ("$($guidance.text)".Trim()) {
        $system += Get-PromptMessage 'probe_guidance' @{ text = "$($guidance.text)" }
        $promptVersion = "probe-5+$($guidance.id)"
    }
}

$user = if ($Draft -and $AgentReport) {
    $rep = Get-Content $AgentReport -Raw | ConvertFrom-Json
    $failed = @($rep.commands | Where-Object { $_.exit -and $_.exit -ne 0 } | Select-Object -Last 8 | ForEach-Object { "- $($_.origin) [exit $($_.exit)]: $(Format-AgentBrief $_.command 200)" })
    Get-PromptMessage 'probe_refine' @{ os_version = "$($rep.machine.os_version)"; gpu = "$($rep.machine.gpu)"; status = "$($rep.status)"
        steps = (($rep.steps.PSObject.Properties | ForEach-Object { "$($_.Name)=$($_.Value)" }) -join ', '); blocked_on = "$($rep.blocked_on)"
        failed = ($failed -join "`n"); summary = "$($rep.summary)"; draft = (Get-Content $Draft -Raw) }
} else {
    Get-PromptMessage 'probe_user' @{ target = $Target; target_here = [bool]($targetDir -or $linuxDir) }
}
if ($PromptOut) {
    [IO.File]::WriteAllText([IO.Path]::GetFullPath($PromptOut), (ConvertTo-Json ([ordered]@{ prompt_version = $promptVersion; system = $system; user = $user; tools = @($tools) }) -Depth 20), [Text.UTF8Encoding]::new($false))
    return
}
if ($ToolCalls) {
    $script:DraftReminded = $false; $script:BestDraft = $null
    $callList = Get-Content $ToolCalls -Raw | ConvertFrom-Json
    $outs = @(foreach ($c in $callList) {
        $o = try { & $dispatch $c.name $c.arguments } catch { "error: $_" }
        "$o"
    })
    $res = [ordered]@{ outputs = $outs; hidden_attempts = $script:HiddenAttempts; withheld_lines = $script:Withheld; guarded_lines = $script:LeakLines.Count; draft_checks = $script:DraftStats }
    [IO.File]::WriteAllText([IO.Path]::GetFullPath("$ToolCalls.out.json"), (ConvertTo-Json $res -Depth 6), [Text.UTF8Encoding]::new($false))
    return
}

# ---- run ------------------------------------------------------------------------

$runId = "$(Get-Date -Format 'yyyyMMdd-HHmmss')-$PID"
$logDir = Join-Path $env:LOCALAPPDATA 'installzero\probe'
New-Item -ItemType Directory -Force $logDir | Out-Null
$transcript = Join-Path $logDir "$runId.jsonl"
$script:DraftStats = $null
Write-AgentLine "InstallZero probe$(if ($Target) { " of $Target" }) -> $Out" 'Cyan'
$session = $null
try {
    $session = Start-AgentLlm $Model $Backend $Remote $LlmUrl $Context $Seed
    $finishGuard = {
        param($A, $Text)
        if (Test-Path $script:Out) {
            $s = $script:DraftStats
            if ($script:FormatPushedBack -or -not $s -or ($s.steps -and -not $s.problems)) { return $null }
            $script:FormatPushedBack = $true
            return Get-PromptMessage 'probe_format_problems' @{ problems = (@(Test-InstallMd (Read-InstallMd $script:Out)) -join '; ') }
        }
        $start = $Text.IndexOf("---`n"); if ($start -lt 0) { $start = $Text.IndexOf("---`r`n") }
        if ($start -ge 0 -and $Text.Substring($start) -match '(?ms)^name\s*:.*^##\s+Step\s+\d+') {
            $script:SavedFromText = $Text
            return Get-PromptMessage 'probe_saved_at_finish' @{ checks = (Invoke-DraftCheck $Text.Substring($start)) }
        }
        Get-PromptMessage 'probe_not_saved'
    }
    $script:DraftReminded = $false; $script:BestDraft = $null; $script:SavedFromText = ''; $script:FormatPushedBack = $false
    $checkpoint = {
        param($Run)
        # A document written as a plain message is saved and checked like a write_install_md call.
        $text = "$($Run.last_long_text)"
        if ($text -and $text -ne $script:SavedFromText) {
            $start = $text.IndexOf("---`n"); if ($start -lt 0) { $start = $text.IndexOf("---`r`n") }
            if ($start -ge 0 -and $text.Substring($start) -match '(?ms)^name\s*:.*^##\s+Step\s+\d+') {
                $script:SavedFromText = $text
                return Get-PromptMessage 'probe_saved_from_message' @{ checks = (Invoke-DraftCheck $text.Substring($start)) }
            }
        }
        if ($script:DraftReminded -or (Test-Path $script:Out) -or $Run.steps -lt [math]::Floor($MaxTurns / 2)) { return $null }
        $script:DraftReminded = $true
        Get-PromptMessage 'probe_halfway' @{ turns = $Run.steps; max_turns = $MaxTurns }
    }
    $run = Invoke-AgentLoop -Session $session -System $system -User $user -Tools $tools -Dispatch $dispatch -MaxTurns $MaxTurns -TimeoutMinutes $TimeoutMinutes -Transcript $transcript -MaxTokens 8192 `
        -FinishGuard $finishGuard -Checkpoint $checkpoint -FinalTools @('write_install_md') -ResultSaved { Test-Path $script:Out } `
        -FinalNudge (Get-PromptMessage 'probe_final') -ParseFailureNudge (Get-PromptMessage 'probe_parse_failure') -UnchangedWriteHint (Get-PromptMessage 'probe_unchanged_hint')
    # Last resort: a document the model wrote as a message is still the result.
    if (-not (Test-Path $script:Out) -and $run.last_long_text) {
        $text = $run.last_long_text
        $start = $text.IndexOf("---`n"); if ($start -lt 0) { $start = $text.IndexOf("---`r`n") }
        if ($start -ge 0 -and $text.Substring($start) -match '(?ms)^name\s*:.*^##\s+Step\s+\d+') {
            Write-AgentLine 'The model wrote the install.md as a message without saving it; saving it now.' 'Yellow'
            [void](Invoke-DraftCheck $text.Substring($start))
        }
    }
    if ($script:BestDraft -and $script:DraftStats -and $script:BestDraft.Stats -ne $script:DraftStats) {
        Write-AgentLine 'The last draft was worse than an earlier one; keeping the earlier draft.' 'Yellow'
        [IO.File]::WriteAllText($script:Out, $script:BestDraft.Content.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false))
        $script:DraftStats = $script:BestDraft.Stats
    }
} finally { if ($session -and $session.Server) { Stop-LlmServer $session.Server } }

$result = [ordered]@{
    run_id = $runId; target = $Target; target_env = $(if ($linux) { "wsl:$Wsl" } else { 'local' }); no_docs = [bool]$NoDocs; out = $(if (Test-Path $Out) { $Out } else { '' }); model = $session.Label; context = $session.Context; prompt_version = $promptVersion
    blind = [ordered]@{ hidden_paths = @($hidden); hidden_attempts = $script:HiddenAttempts; withheld_lines = $script:Withheld; guarded_lines = $script:LeakLines.Count }
    saved = [bool](Test-Path $Out); finished = $run.finished; stop_reason = $run.stop_reason; summary = $(if ($run.Finish) { "$($run.Finish.summary)" } else { '' }); gaps = $(if ($run.Finish) { "$($run.Finish.gaps)" } else { '' }); confidence = $(if ($run.Finish) { "$($run.Finish.confidence)" } else { '' })
    draft_checks = $script:DraftStats
    stats = [ordered]@{ tool_calls = $run.tool_calls; model_turns = $run.steps; invalid_calls = $run.invalid_calls; repeats = $run.repeats; nudges = $run.nudges; seconds = $run.seconds; prompt_tokens = $run.prompt_tokens; completion_tokens = $run.completion_tokens; server_errors = $run.server_errors; error = $run.error }
    transcript = $transcript
}
if (-not $ResultFile) { $ResultFile = Join-Path $logDir "$runId.result.json" }
$result | ConvertTo-Json -Depth 6 | Set-Content -Path $ResultFile -Encoding UTF8
Write-AgentLine ''
if ($result.out) { Write-AgentLine "install.md: $Out ($(if ($script:DraftStats) { "$($script:DraftStats.steps) steps; checks here: $($script:DraftStats.passed) passed, $($script:DraftStats.failed) failed, $($script:DraftStats.untested) untested" }))" 'Green' } else { Write-AgentLine 'No install.md was written.' 'Yellow' }
if ($result.gaps) { Write-AgentLine "Gaps: $($result.gaps)" }
Write-AgentLine "Blind to install files: $($script:HiddenAttempts) attempt(s) to reach hidden files refused, $($script:Withheld) line(s) of install-file content withheld ($($script:LeakLines.Count) guarded lines)."
Write-AgentLine "Result: $ResultFile"
