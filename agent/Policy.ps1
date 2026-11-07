# Safety tiers for commands the agent runs (INSTALL-MD.md, "Safety"):
#   read     read-only (Tools.ps1's Test-ReadOnlyCommand)      runs
#   project  changes confined to the install folder            runs
#   system   machine-wide or unrecognised                      asks Y/N
#   never    destructive                                       refused
# Unknown commands are system, so a new tool is asked about, never assumed safe.
# Requires Tools.ps1.

$script:NeverPatterns = @(
    @{ Re = '(?i)\b(Format-Volume|Clear-Disk|Remove-Partition|Initialize-Disk|diskpart|bcdedit|vssadmin\s+delete|cipher(\.exe)?\s+/w)\b'; Why = 'changes disks, partitions or boot settings' }
    @{ Re = '(?i)\bformat(\.com)?\s+[a-z]:'; Why = 'formats a drive' }
    @{ Re = '(?i)\bwsl(\.exe)?\s+--unregister\b'; Why = 'deletes a WSL distro and everything in it' }
    @{ Re = '(?i)\bdocker\s+(volume\s+(rm|prune)|system\s+prune\b.*--volumes)'; Why = 'deletes Docker volumes (data)' }
    @{ Re = '(?i)\breg(\.exe)?\s+delete\s+HK(LM|EY_LOCAL_MACHINE)'; Why = 'deletes machine registry keys' }
    @{ Re = '(?i)\b(cmdkey|Remove-StoredCredential)\b|\bcredential\s+manager\b'; Why = 'touches stored credentials' }
    @{ Re = '(?i)\brm\s+-[a-z]*r[a-z]*f?[a-z]*\s+(/|~|\$HOME)(\s|$)'; Why = 'deletes a home or root folder' }
    @{ Re = '(?i)\b(Restart-Computer|Stop-Computer)\b|\bshutdown(\.exe)?\s+/[rsp]'; Why = 'restarts or shuts down the PC; ask the user to do it' }
)

$script:SystemPatterns = @(
    '(?i)\b(winget|choco|scoop|msiexec|dism|apt(-get)?|dnf|yum|pacman|brew|sudo|snap)\b'
    '(?i)\bwsl(\.exe)?\s+--(install|update|shutdown|set-default|set-version|import|export)'
    '(?i)\b(Enable|Disable)-WindowsOptionalFeature\b|\bAdd-WindowsCapability\b'
    '(?i)\b(Set|Start|Stop|Restart|New|Remove)-Service\b|\bsc(\.exe)\s+(config|create|delete|start|stop)'
    '(?i)\bnetsh\b(?!.*\bshow\b)|\bNew-NetFirewallRule\b|\bpowercfg\b|\bsetx\b'
    '(?i)\b(Set|New|Remove)-ItemProperty\b.*\bHK(LM|EY_LOCAL_MACHINE|CR)\b|\breg(\.exe)?\s+add\s+HK(LM|EY_LOCAL_MACHINE)'
    '(?i)\b(Add|Remove)-LocalGroupMember\b|\bnet\s+(localgroup|user)\b'
    '(?i)-Verb\s+RunAs\b'
    '(?i)\bnpm\s+(i|install)\s+(-g|--global)\b|\bpip3?\s+install\b.*--user\b|\bInstall-Module\b'
    '(?i)\[Environment\]::SetEnvironmentVariable'
)

# Commands that only act on the current folder or on paths named in the
# command; with the path check below they stay inside the install folder.
$script:ProjectCommands = '(?i)^(git|docker|docker-compose|python|python3|py|pip|pip3|uv|conda|mamba|npm|npx|pnpm|yarn|node|cargo|go|dotnet|make|cmake|curl|wget|tar|unzip|7z|New-Item|mkdir|md|Set-Content|Add-Content|Out-File|Copy-Item|cp|copy|Move-Item|mv|move|Remove-Item|rm|del|rmdir|Rename-Item|Expand-Archive|Compress-Archive|Invoke-WebRequest|iwr|Invoke-RestMethod|irm|Set-Location|cd|Push-Location|Pop-Location|Write-Host|Write-Output|Write-(Error|Warning|Verbose|Debug|Information|Progress)|Out-Null|Out-String|Format-\w+|Sort-Object|Measure-Object|Group-Object|Compare-Object|Tee-Object|Select-String|ConvertTo-\w+|ConvertFrom-\w+|Unblock-File|Wait-Process|(Set|Clear|Remove)-Variable|echo|Start-Sleep|ForEach-Object|Where-Object|%|\?|Select-Object|Test-Path|Join-Path|Get-\w+|Resolve-Path|Split-Path|\.\\[\w.-]+\.(ps1|bat|cmd|exe)|[\w.-]+\.(bat|cmd))$'

function Get-CommandPaths([string]$Command) {
    $paths = [System.Collections.Generic.List[string]]::new()
    foreach ($m in [regex]::Matches($Command, '(?i)(?<![\w/])([a-z]:[\\/][^\s''"`|;,)]*)')) { $paths.Add($m.Groups[1].Value) }
    foreach ($m in [regex]::Matches($Command, '(?i)(\$env:(USERPROFILE|APPDATA|LOCALAPPDATA|ProgramFiles|ProgramData|SystemRoot|windir|TEMP)|%(USERPROFILE|APPDATA|LOCALAPPDATA|ProgramFiles|ProgramData|SystemRoot|windir|TEMP)%|~[\\/])[^\s''"`|;,)]*')) { $paths.Add($m.Value) }
    $paths.ToArray()
}

function Test-InsideFolder([string]$Path, [string[]]$Roots) {
    $expanded = $Path -replace '(?i)^\$env:(\w+)', { [Environment]::GetEnvironmentVariable($_.Groups[1].Value) } -replace '(?i)^%(\w+)%', { [Environment]::GetEnvironmentVariable($_.Groups[1].Value) } -replace '^~', $env:USERPROFILE
    $full = try { [IO.Path]::GetFullPath($expanded.TrimEnd('\', '/')) } catch { return $false }
    foreach ($r in $Roots) {
        $root = [IO.Path]::GetFullPath($r.TrimEnd('\', '/'))
        if ($full -eq $root -or $full.StartsWith($root + '\', 'OrdinalIgnoreCase')) { return $true }
    }
    $false
}

# Returns @{ Tier; Reason }. $Roots are the folders project-tier changes may
# touch (the install folder, the agent's temp folder).
function Get-CommandTier([string]$Command, [string]$Shell = 'powershell', [string[]]$Roots = @()) {
    foreach ($n in $script:NeverPatterns) { if ($Command -match $n.Re) { return @{ Tier = 'never'; Reason = $n.Why } } }
    if ($Shell -in 'powershell', 'pwsh', '' -and -not (Test-ReadOnlyCommand $Command)) { return @{ Tier = 'read'; Reason = 'read-only' } }
    foreach ($s in $script:SystemPatterns) { if ($Command -match $s) { return @{ Tier = 'system'; Reason = "machine-wide change ($($Matches[0].Trim()))" } } }
    $outside = @(Get-CommandPaths $Command | Where-Object { -not (Test-InsideFolder $_ $Roots) })
    if ($outside) { return @{ Tier = 'system'; Reason = "touches paths outside the install folder: $($outside -join ', ')" } }
    if ($Shell -in 'powershell', 'pwsh', '') {
        $tokens = $null; $errs = $null
        $ast = [System.Management.Automation.Language.Parser]::ParseInput($Command, [ref]$tokens, [ref]$errs)
        if ($errs.Count) { return @{ Tier = 'system'; Reason = "does not parse: $($errs[0].Message)" } }
        foreach ($c in $ast.FindAll({ $args[0] -is [System.Management.Automation.Language.CommandAst] }, $true)) {
            $name = $c.GetCommandName()
            if (-not $name) { return @{ Tier = 'system'; Reason = "runs a computed command ($($c.Extent.Text))" } }
            if ($name -match '(?i)^(Start-Process|saps|start)$') {
                $els = @($c.CommandElements | Select-Object -Skip 1)
                $i = [array]::FindIndex([object[]]$els, [Predicate[object]]{ param($e) $e -is [System.Management.Automation.Language.CommandParameterAst] -and $e.ParameterName -match '^(?i)f(i(l(e(p(a(th?)?)?)?)?)?)?$' })
                $exe = if ($i -ge 0 -and $i + 1 -lt $els.Count) { $els[$i + 1] } else { $els | Where-Object { $_ -isnot [System.Management.Automation.Language.CommandParameterAst] } | Select-Object -First 1 }
                $exePath = if ($exe -is [System.Management.Automation.Language.StringConstantExpressionAst]) { $exe.Value } else { "$($exe.Extent.Text)" }
                if (-not ($exePath -match '[\\/]' -and (Test-InsideFolder $exePath $Roots))) { return @{ Tier = 'system'; Reason = "starts a program outside the install folder ($exePath)" } }
                continue
            }
            # A program inside the install folder (a venv's pip, a project script).
            $inProject = $name -match '[\\/]' -and (($name -notmatch '^(?i)[a-z]:|^[\\/]|^~|^\$') -or (Test-InsideFolder $name $Roots))
            if (-not $inProject -and $name -notmatch $script:ProjectCommands) { return @{ Tier = 'system'; Reason = "'$name' is not a known project-folder command" } }
            if ($name -match '(?i)(^|[\\/])(pip|pip3)(\.exe)?$' -and -not ($Command -match '(?i)(venv|\.venv|env)[\\/]')) { return @{ Tier = 'system'; Reason = 'pip outside a virtual environment installs for the whole machine; use the venv''s pip' } }
        }
        return @{ Tier = 'project'; Reason = 'changes only the install folder' }
    }
    foreach ($word in @([regex]::Matches($Command, '(?m)(^|[;&|]\s*)([\w./-]+)') | ForEach-Object { $_.Groups[2].Value })) {
        if ($word -notmatch $script:ProjectCommands -and $word -notin 'cd', 'export', 'source', '.', 'set', 'echo', 'test', '[', 'true', 'false') { return @{ Tier = 'system'; Reason = "'$word' is not a known project-folder command" } }
    }
    @{ Tier = 'project'; Reason = 'changes only the install folder' }
}
