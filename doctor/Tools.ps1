# Tools the install doctor's model can call. Diagnosis only: nothing here
# changes the machine, and run_command refuses anything it cannot prove is
# read-only. Dot-source this file and set $script:DoctorRepoDir first.

$script:DoctorRepoDir = if ($script:DoctorRepoDir) { $script:DoctorRepoDir } else { (Get-Location).Path }
$script:MaxToolChars = 6000

# ---- compose ---------------------------------------------------------------

# The files the running project was started with (docker compose ls), so
# overlays such as compose/scale.yml are honoured; falls back to the defaults.
function Get-RepoComposeArgs([string]$RepoDir = $script:DoctorRepoDir) {
    $main = Join-Path $RepoDir 'docker-compose.yml'
    try {
        $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
        $projects = & docker compose ls -a --format json 2>$null | ConvertFrom-Json
        $ErrorActionPreference = $old
        foreach ($p in @($projects)) {
            $files = @("$($p.ConfigFiles)" -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
            if ($files | Where-Object { $_ -ieq $main }) {
                return @('compose') + @($files | ForEach-Object { '-f'; $_ })
            }
        }
    } catch { }
    $compose = @('compose', '-f', $main)
    $scale = Join-Path $RepoDir 'compose\scale.yml'
    if (Test-Path $scale) { $compose += @('-f', $scale) }
    $compose
}

function Invoke-Docker([string[]]$DockerArgs, [string]$RepoDir = $script:DoctorRepoDir) {
    Push-Location $RepoDir
    try {
        $old = $ErrorActionPreference; $ErrorActionPreference = 'Continue'
        $out = & docker @DockerArgs 2>&1 | ForEach-Object { "$_" } | Out-String
        [pscustomobject]@{ Code = $LASTEXITCODE; Out = $out }
    } finally { $ErrorActionPreference = $old; Pop-Location }
}

# ---- read-only command policy ----------------------------------------------

$script:ReadOnlyCmdlets = @(
    'Out-String', 'Out-Null', 'Out-Host', 'Write-Output', 'ForEach-Object', 'Where-Object', 'Select-Object', 'Sort-Object', 'Group-Object',
    'Measure-Object', 'Format-Table', 'Format-List', 'Format-Wide', 'Compare-Object', 'Select-String',
    'foreach', '%', 'where', '?', 'select', 'sort', 'group', 'measure', 'ft', 'fl', 'fw', 'sls', 'echo', 'write',
    'dir', 'ls', 'gci', 'cat', 'type', 'gc', 'gps', 'ps', 'gsv', 'gcm', 'gi', 'gp', 'gwmi', 'gcim', 'diff',
    'findstr', 'findstr.exe', 'where.exe', 'whoami', 'whoami.exe', 'hostname', 'hostname.exe', 'systeminfo',
    'systeminfo.exe', 'ipconfig', 'ipconfig.exe', 'netstat', 'netstat.exe', 'tasklist', 'tasklist.exe',
    'nvidia-smi', 'nvidia-smi.exe', 'driverquery', 'driverquery.exe', 'nslookup', 'nslookup.exe', 'ping', 'ping.exe',
    'query', 'quser', 'qwinsta'
)
$script:ReadOnlyVerbs = 'Get|Test|Select|Measure|Compare|ConvertTo|ConvertFrom|Resolve|Split|Join|Find|Format|Out-String|Read'
$script:DeniedCmdlets = @('Read-Host', 'Get-Credential', 'Invoke-Expression', 'iex', 'Invoke-Command', 'icm', 'Start-Process', 'saps', 'start',
    'Format-Volume', 'Format-Hex', 'Tee-Object', 'tee')
# Programs allowed with any arguments; docker, git, wsl, netsh, sc.exe, reg,
# icacls and powercfg are allowed only for read-only subcommands
# (Test-NativeArgsReadOnly).
$script:SubcommandChecked = @('docker', 'git', 'wsl', 'netsh', 'sc', 'reg', 'icacls', 'powercfg')
$script:AllowedMethods = @(
    'ToString', 'Trim', 'TrimStart', 'TrimEnd', 'Split', 'Replace', 'Substring', 'Contains', 'StartsWith', 'EndsWith',
    'ToLower', 'ToUpper', 'ToLowerInvariant', 'ToUpperInvariant', 'IndexOf', 'LastIndexOf', 'Equals', 'PadLeft', 'PadRight',
    'GetType', 'ToArray', 'Join', 'Format', 'IsNullOrEmpty', 'IsNullOrWhiteSpace', 'GetEnvironmentVariable', 'GetFolderPath',
    'GetHostAddresses', 'GetHostEntry', 'Exists', 'GetFiles', 'GetDirectories', 'ReadAllText', 'ReadAllLines', 'Parse',
    'TryParse', 'Round', 'Floor', 'Ceiling', 'Abs', 'Min', 'Max', 'GetOwner', 'Matches', 'Match', 'IsMatch', 'Escape',
    'FromFileTime', 'GetValueNames', 'GetValue', 'GetSubKeyNames', 'GetCurrent', 'IsInRole', 'GetNetworkInterfaces', 'GetIPProperties',
    'GetActiveTcpListeners', 'GetIPGlobalProperties', 'Count', 'Where', 'ForEach'
)

function Test-NativeArgsReadOnly([string]$Name, [string[]]$Arguments) {
    $n = $Name.ToLowerInvariant() -replace '\.exe$', ''
    $positional = @($Arguments | Where-Object { $_ -notmatch '^-' })
    switch ($n) {
        'docker' {
            # skip global options and their values
            $a = [System.Collections.Generic.List[string]]$Arguments
            $i = 0
            while ($i -lt $a.Count -and $a[$i] -match '^-') { if ($a[$i] -match '^--?(context|host|config|log-level|H|c|l)$') { $i++ }; $i++ }
            if ($i -ge $a.Count) { return $true }
            $sub = $a[$i]
            $rest = @(if ($i + 1 -lt $a.Count) { $a.GetRange($i + 1, $a.Count - $i - 1) })
            if ($sub -in 'ps', 'logs', 'inspect', 'images', 'info', 'version', 'top', 'port', 'history') { return $true }
            if ($sub -eq 'stats') { return [bool]($rest -contains '--no-stream') }
            if ($sub -in 'network', 'volume', 'image', 'container', 'context', 'system', 'buildx', 'builder') {
                $verb = @($rest | Where-Object { $_ -notmatch '^-' })[0]
                return $verb -in 'ls', 'list', 'inspect', 'logs', 'df', 'info', 'version', 'show', 'du', 'history', 'top', 'port'
            }
            if ($sub -eq 'compose') {
                $j = 0
                while ($j -lt $rest.Count -and $rest[$j] -match '^-') { if ($rest[$j] -match '^(-f|--file|-p|--project-name|--project-directory|--env-file|--profile|--progress)$') { $j++ }; $j++ }
                if ($j -ge $rest.Count) { return $true }
                return $rest[$j] -in 'ps', 'logs', 'config', 'images', 'ls', 'top', 'version', 'port'
            }
            return $false
        }
        'git' { $sub = $positional[0]; return ($sub -in 'status', 'log', 'diff', 'show', 'rev-parse', 'describe', 'ls-files', 'blame', 'remote', 'config') -and -not ($Arguments -match '^(--add|--unset|--replace-all|set-url|add|remove|rename|prune)$') -and -not ($sub -eq 'config' -and ($Arguments -notmatch '^(--get|--list|-l|--get-all|--show-origin)$')) }
        'wsl' { return -not ($Arguments | Where-Object { $_ -notmatch '^(-l|--list|-v|--verbose|--status|--version|-q|--quiet|--running|--all)$' }) }
        'netsh' { return [bool]($Arguments -contains 'show') -and -not ($Arguments -match '^(add|delete|set|reset|dump)$') }
        'sc' { return $positional[0] -in 'query', 'queryex', 'qc', 'qdescription', 'getdisplayname' }
        'reg' { return $positional[0] -eq 'query' }
        'icacls' { return -not ($Arguments | Where-Object { $_ -match '^/' -and $_ -notmatch '^/(t|c|l|q)$' }) }
        'powercfg' {
            # Positional arguments (scheme, subgroup, setting) only follow /query.
            $switches = @($Arguments | Where-Object { $_ -match '^[/-]' })
            $values = @($Arguments | Where-Object { $_ -notmatch '^[/-]' })
            if ($switches.Count -ne 1 -or $switches[0] -notmatch '^[/-](getactivescheme|list|l|query|q|a|availablesleepstates)$') { return $false }
            return ($values.Count -eq 0) -or ($switches[0] -match '^[/-](query|q)$')
        }
        default { return $false }
    }
}

# Returns $null when the command is allowed, otherwise the reason it is not.
function Test-ReadOnlyCommand([string]$Command) {
    $tokens = $null; $errs = $null
    $ast = [System.Management.Automation.Language.Parser]::ParseInput($Command, [ref]$tokens, [ref]$errs)
    if ($errs -and $errs.Count) { return "does not parse: $($errs[0].Message)" }
    if ($Command -match $script:SecretFileInCommandRegex) { return 'names a file that holds secrets; use read_file, which shows .env with secret values masked' }
    if ($Command -match $script:EnvDriveRegex) { return 'lists environment variables (env: drive); read single non-secret ones as $env:NAME' }
    foreach ($v in $ast.FindAll({ $args[0] -is [System.Management.Automation.Language.VariableExpressionAst] }, $true)) {
        if ($v.VariablePath.DriveName -eq 'env' -and (Test-SecretName ($v.VariablePath.UserPath -replace '^(?i)env:', ''))) { return "reads the secret environment variable $($v.Extent.Text)" }
    }
    foreach ($r in $ast.FindAll({ $args[0] -is [System.Management.Automation.Language.FileRedirectionAst] }, $true)) {
        if ($r.Location.Extent.Text -ne '$null') { return "writes to a file ($($r.Extent.Text))" }
    }
    foreach ($m in $ast.FindAll({ $args[0] -is [System.Management.Automation.Language.InvokeMemberExpressionAst] }, $true)) {
        $name = $m.Member.Extent.Text.Trim("'`"")
        if ($script:AllowedMethods -notcontains $name) { return "calls method '$name', which is not on the read-only list" }
    }
    foreach ($c in $ast.FindAll({ $args[0] -is [System.Management.Automation.Language.CommandAst] }, $true)) {
        $name = $c.GetCommandName()
        if (-not $name) { return "runs a command whose name is computed at run time ($($c.Extent.Text))" }
        if ($c.InvocationOperator -eq 'Dot') { return "dot-sources '$name'" }
        if ($script:DeniedCmdlets -contains $name) { return "'$name' is not allowed" }
        $isCmdlet = $name -match "^($script:ReadOnlyVerbs)-" -or $script:ReadOnlyCmdlets -contains $name
        $argText = @($c.CommandElements | Select-Object -Skip 1 | ForEach-Object {
            if ($_ -is [System.Management.Automation.Language.StringConstantExpressionAst]) { $_.Value } else { $_.Extent.Text } })
        if ($name -in 'Invoke-RestMethod', 'irm', 'Invoke-WebRequest', 'iwr', 'curl', 'wget') {
            if ($argText -match '^-(Method|Body|InFile|OutFile|Form)$') { return "$name may only make plain GET requests" }
            if (-not ($argText -match '^https?://(localhost|127\.0\.0\.1)([:/]|$)')) { return "$name may only call localhost" }
            continue
        }
        if ($name -in 'cmd', 'cmd.exe') { return 'cmd is not allowed; use PowerShell' }
        if ($name -eq 'sc') { return "'sc' is PowerShell's alias for Set-Content; use sc.exe" }
        if ($name -match '^(Set|New|Remove|Stop|Start|Restart|Clear|Add|Move|Copy|Rename|Install|Uninstall|Enable|Disable|Register|Unregister|Invoke|Update|Write|Out|Export|Import|Push|Pop|Suspend|Resume|Grant|Revoke|Mount|Dismount|Reset|Initialize|Format-Volume|Lock|Unlock|Send|Receive|Wait|Debug|Use|Protect|Unprotect|Publish|Unpublish|Save|Sync|Submit|Approve|Deny|Edit|Expand|Compress|Merge|Optimize|Repair|Restore|Backup|Checkpoint|Close|Open|Connect|Disconnect|Block|Unblock|Hide|Show|Join-Domain)-' -and $name -notmatch "^($script:ReadOnlyVerbs)-" -and $script:ReadOnlyCmdlets -notcontains $name) {
            return "'$name' changes state"
        }
        if ($isCmdlet) { continue }
        if ($script:SubcommandChecked -contains ($name.ToLowerInvariant() -replace '\.exe$', '')) {
            if (Test-NativeArgsReadOnly $name $argText) { continue }
            return "'$($c.Extent.Text)' is not a read-only use of $name"
        }
        return "'$name' is not on the read-only list"
    }
    $null
}

# ---- running commands ------------------------------------------------------

function Limit-Text([string]$Text, [int]$Max = $script:MaxToolChars) {
    if ($Text.Length -le $Max) { return $Text }
    $head = [int]($Max / 3); $tail = $Max - $head
    $Text.Substring(0, $head) + "`n... [$($Text.Length - $Max) characters omitted] ...`n" + $Text.Substring($Text.Length - $tail)
}

function Invoke-IsolatedPowerShell([string]$Command, [int]$TimeoutSec = 60, [string]$WorkingDirectory = $script:DoctorRepoDir) {
    $script = "[Console]::OutputEncoding = [Text.Encoding]::UTF8; `$ProgressPreference = 'SilentlyContinue'; " + $Command
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = Join-Path $PSHOME 'powershell.exe'
    $psi.Arguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -EncodedCommand ' + [Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($script))
    $psi.WorkingDirectory = $WorkingDirectory
    $psi.UseShellExecute = $false
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    $psi.StandardOutputEncoding = [Text.Encoding]::UTF8
    $psi.StandardErrorEncoding = [Text.Encoding]::UTF8
    $psi.CreateNoWindow = $true
    foreach ($k in @($psi.EnvironmentVariables.Keys)) { if (Test-SecretName $k) { $psi.EnvironmentVariables.Remove($k) } }
    $p = [System.Diagnostics.Process]::Start($psi)
    $outTask = $p.StandardOutput.ReadToEndAsync()
    $errTask = $p.StandardError.ReadToEndAsync()
    if (-not $p.WaitForExit($TimeoutSec * 1000)) {
        & taskkill.exe /PID $p.Id /T /F 2>&1 | Out-Null
        return "[timed out after $TimeoutSec s]`n" + $outTask.Result + $errTask.Result
    }
    $text = $outTask.Result
    if ($errTask.Result.Trim()) { $text += "`n[stderr]`n" + $errTask.Result }
    "[exit $($p.ExitCode)]`n" + $text
}

# ---- tools -----------------------------------------------------------------

# The doctor's own files (this folder with its eval scenarios, the model
# assets, past transcripts and results) are not part of the install; keeping
# them out of view also stops the model reading how test faults were planted.
$script:HiddenPathRegex = 'scripts(\\|/)install(\\|/)doctor((\\|/)|\b)|scripts(\\|/)install(\\|/)agent(\\|/)eval|installzero(\\|/)eval|doctor-assets|rl-roboracer(\\|/)(doctor|eval-stash)\b|eval-results\.jsonl|Scenarios\.ps1|Invoke-DoctorEval'

function Test-HiddenPath([string]$Path) { $Path -match $script:HiddenPathRegex }

# ---- secrets -----------------------------------------------------------------

# Secret values must never reach the model: a hosted model sends them to its
# provider, and transcripts keep them. Four layers: secret files are not
# readable (except .env, shown with values masked), run_command may not name
# them or read secret environment variables, its processes start without
# those variables, and every tool output is redacted against the actual
# values and known key formats.

$script:SecretPathRegex = '(?i)((^|[\\/])\.env(\.(?!(example|sample|template)$)[^\\/]+)?$|(^|[\\/])secrets[\\/].|\.(pem|key|pfx|p12)$|(^|[\\/])id_(rsa|dsa|ecdsa|ed25519)|(^|[\\/])\.ssh[\\/]|\.git-credentials$|(^|[\\/])\.(netrc|npmrc|pypirc)$|\.aws[\\/]credentials$|\.docker[\\/]config\.json$)'
$script:EnvFileRegex = '(?i)(^|[\\/])\.env(\.(?!(example|sample|template)$)[^\\/]+)?$'
# Names whose values are secrets (API_KEY, ..._TOKEN_SECRET, SMTP_PASSWORD);
# not ..._TOKEN_TTL_SECONDS.
$script:SecretNameRegex = '(?i)(^|_)(API_?KEY|KEY|SECRET|TOKEN|PASSWORD|PASSWD|PWD|PASS|CREDENTIALS?|COOKIE|AUTH)$'
# A secret file named in a command, and the env: drive used as a path
# (Get-ChildItem env:). $env:NAME is checked by name in Test-ReadOnlyCommand.
$script:SecretFileInCommandRegex = '(?i)((^|[\s''"\\/(=,])\.env(?!\.(example|sample|template)\b)(\b|$)|(^|[\s''"\\/(=,])secrets[\\/]|\.(pem|pfx|p12)\b|id_(rsa|dsa|ecdsa|ed25519)\b|\.ssh[\\/]|\.git-credentials|\.(netrc|npmrc|pypirc)\b|\.aws[\\/]credentials|\.docker[\\/]config\.json)'
$script:EnvDriveRegex = '(?i)(?<![\$\w{])env:'

$script:SecretPatterns = @(
    @('sk-ant-[A-Za-z0-9_\-]{20,}', '<secret>'),
    @('\bsk-(proj-|svcacct-)?[A-Za-z0-9_\-]{20,}', '<secret>'),
    @('\bxai-[A-Za-z0-9_\-]{20,}', '<secret>'),
    @('\bAIza[0-9A-Za-z_\-]{30,}', '<secret>'),
    @('\bgh[pousr]_[A-Za-z0-9]{30,}', '<secret>'),
    @('\bgithub_pat_[A-Za-z0-9_]{30,}', '<secret>'),
    @('\bglpat-[A-Za-z0-9_\-]{20,}', '<secret>'),
    @('\b(AKIA|ASIA)[0-9A-Z]{16}\b', '<secret>'),
    @('\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}', '<secret>'),
    @('(?i)(\bBearer\s+)[A-Za-z0-9._~+/\-]{16,}=*', '$1<secret>'),
    @('(://[^/\s:@]+:)[^@\s/]+@', '$1<secret>@'),
    @('(?i)((?<!<)(password|passwd|pwd|secret|api[_-]?key|access[_-]?token)["'']?\s*[:=]\s*["'']?)(?!<secret)[^\s"'',;}\\<>]{4,}', '$1<secret>')
)

function Test-SecretPath([string]$Path) { $Path -match $script:SecretPathRegex }
function Test-SecretName([string]$Name) { $Name -match $script:SecretNameRegex }

function Get-EnvFileEntries([string]$Path) {
    foreach ($line in @(Get-Content -LiteralPath $Path -ErrorAction SilentlyContinue)) {
        if ($line -match '^\s*(export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$') {
            [pscustomobject]@{ Name = $Matches[2]; Value = $Matches[3].Trim().Trim('"').Trim("'"); Line = $line }
        } else { [pscustomobject]@{ Name = ''; Value = ''; Line = $line } }
    }
}

# Literal secret values to redact, longest first: secret-named variables in
# the repo's .env files and in the process, user and machine environment,
# plus passwords embedded in URLs there. Cached per repo folder.
function Get-SecretValues {
    if ($script:SecretValuesFor -eq $script:DoctorRepoDir -and $script:SecretValues) { return $script:SecretValues }
    $found = @{}
    $add = { param($name, $value)
        if ($value -match '://[^/\s:@]+:([^@\s/]+)@' -and $Matches[1].Length -ge 4) { $found[$Matches[1]] = $name }
        if ((Test-SecretName $name) -and $value.Length -ge 6) { $found[$value] = $name }
    }
    $envFiles = @(Get-ChildItem -LiteralPath $script:DoctorRepoDir -Force -File -Filter '.env*' -ErrorAction SilentlyContinue | Where-Object { $_.Name -match $script:EnvFileRegex })
    foreach ($f in $envFiles) { foreach ($e in Get-EnvFileEntries $f.FullName) { if ($e.Name) { & $add $e.Name $e.Value } } }
    foreach ($scope in 'Process', 'User', 'Machine') {
        $vars = [Environment]::GetEnvironmentVariables($scope)
        foreach ($k in $vars.Keys) { & $add "$k" "$($vars[$k])" }
    }
    $script:SecretValues = @($found.GetEnumerator() | Sort-Object { $_.Key.Length } -Descending | ForEach-Object { [pscustomobject]@{ Value = $_.Key; Name = $_.Value } })
    $script:SecretValuesFor = $script:DoctorRepoDir
    $script:SecretValues
}

function Protect-Secrets([string]$Text) {
    if (-not $Text) { return $Text }
    foreach ($s in Get-SecretValues) { $Text = $Text.Replace($s.Value, "<secret:$($s.Name)>") }
    foreach ($p in $script:SecretPatterns) { $Text = $Text -replace $p[0], $p[1] }
    $Text
}

# .env as the model sees it: every name, values of secret-named variables
# replaced by their length, the rest redacted like any other output.
function Get-MaskedEnvFile([string]$Path) {
    $i = 0
    $lines = foreach ($e in Get-EnvFileEntries $Path) {
        $i++
        $text = if ($e.Name -and (Test-SecretName $e.Name)) {
            "$($e.Name)=$(if ($e.Value) { "<secret, $($e.Value.Length) characters>" } else { '' })"
        } else { $e.Line }
        '{0,5}| {1}' -f $i, $text
    }
    "$Path (secret values masked; empty means the variable is set but blank)`n" + ($lines -join "`n")
}

function Remove-HiddenLines([string]$Text) {
    (($Text -split "`r?`n") | Where-Object { $_ -notmatch $script:HiddenPathRegex -and $_ -notmatch '^\?\?\s+scripts/install/doctor/' }) -join "`n"
}

function Resolve-DoctorPath([string]$Path) {
    if (-not $Path) { return $script:DoctorRepoDir }
    $expanded = [Environment]::ExpandEnvironmentVariables($Path)
    if ([IO.Path]::IsPathRooted($expanded)) { return $expanded }
    Join-Path $script:DoctorRepoDir $expanded
}

function Get-SystemFacts {
    $os = Get-CimInstance Win32_OperatingSystem
    $cs = Get-CimInstance Win32_ComputerSystem
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add("Windows: $($os.Caption) build $($os.BuildNumber); user $env:USERDOMAIN\$env:USERNAME (session $((Get-Process -Id $PID).SessionId))")
    $lines.Add("RAM: $([math]::Round($cs.TotalPhysicalMemory / 1GB, 1)) GB total, $([math]::Round($os.FreePhysicalMemory / 1MB, 1)) GB free; CPU: $((Get-CimInstance Win32_Processor | Select-Object -First 1).Name)")
    $gpu = Get-GpuInfo
    $lines.Add($(if ($gpu) { "GPU: $($gpu.Name), driver $($gpu.Driver), $($gpu.FreeMB)/$($gpu.TotalMB) MB free" } else { 'GPU: nvidia-smi not available or failing' }))
    $drive = Get-PSDrive ((Split-Path $script:DoctorRepoDir -Qualifier).TrimEnd(':')) -ErrorAction SilentlyContinue
    if ($drive) { $lines.Add("Disk $($drive.Name): $([math]::Round($drive.Free / 1GB)) GB free") }
    $lines.Add("Repo: $script:DoctorRepoDir")

    $dd = @(Get-CimInstance Win32_Process -Filter "Name = 'Docker Desktop.exe'" -ErrorAction SilentlyContinue)
    if ($dd) {
        $owners = $dd | ForEach-Object { $o = Invoke-CimMethod -InputObject $_ -MethodName GetOwner -ErrorAction SilentlyContinue; "session $($_.SessionId)$(if ($o.User) { " ($($o.User))" } else { ' (another user)' })" } | Sort-Object -Unique
        $lines.Add("Docker Desktop processes: $($owners -join ', ')")
    } else { $lines.Add('Docker Desktop: not running') }
    $info = Invoke-Docker @('info', '--format', '{{.ServerVersion}} | {{.OperatingSystem}} | CPUs {{.NCPU}} | mem {{.MemTotal}}')
    $lines.Add("docker info: $(if ($info.Code -eq 0) { $info.Out.Trim() } else { 'FAILED: ' + (Limit-Text $info.Out.Trim() 400) })")
    $wsl = Invoke-IsolatedPowerShell 'wsl -l -v' 20
    $lines.Add("WSL distros:`n" + (($wsl -replace "`0", '') -replace '\[exit \d+\]\s*', '').Trim())

    $excluded = @(& netsh interface ipv4 show excludedportrange protocol=tcp | Where-Object { $_ -match '^\s*\d+\s+\d+' } | ForEach-Object { ($_.Trim() -split '\s+')[0..1] -join '-' })
    $lines.Add("Windows excluded TCP port ranges: $($excluded -join ', ')")

    $wrappers = @(Get-CimInstance Win32_Process -Filter "Name = 'powershell.exe' OR Name = 'pwsh.exe'" -ErrorAction SilentlyContinue | Where-Object { $_.CommandLine -match 'RunClientWrapper|Start-ClientAtLogon|Watchdog' })
    $lines.Add("Supervisor scripts running: $(if ($wrappers) { ($wrappers | ForEach-Object { if ($_.CommandLine -match '(RunClientWrapper|Start-ClientAtLogon|Watchdog)\.ps1(.*?-Index (\d+))?') { "$($Matches[1])$(if ($Matches[3]) { " #$($Matches[3])" }) (pid $($_.ProcessId), session $($_.SessionId))" } }) -join '; ' } else { 'none' })")
    $unity = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '\.exe$' -and $_.CommandLine -match '--ros-port' })
    $lines.Add("Unity clients running: $(if ($unity) { ($unity | ForEach-Object { "$($_.Name) pid $($_.ProcessId) session $($_.SessionId) started $($_.CreationDate.ToString('s')) ros-port $(if ($_.CommandLine -match '--ros-port (\d+)') { $Matches[1] })" }) -join '; ' } else { 'none' })")
    $lines -join "`n"
}

# NVIDIA's own driver lookup (the service behind nvidia.com/Download). Only the
# GPU model leaves the PC; the URLs are fixed here, not chosen by the model.
$script:NvidiaLookup = 'https://www.nvidia.com/Download/API/lookupValueSearch.aspx?TypeID=3'
$script:NvidiaDriverService = 'https://gfwsl.geforce.com/services_toolkit/services/com/nvidia/services/AjaxDriverService.php?func=DriverManualLookup&osID=57&languageCode=1033&dch=1&sort1=0&numberOfResults=1'

# The project's GPU components (scripts\install\requirements.json, shared with
# the installer's preflight). The doctor sits in scripts\install\doctor in the
# repo and in <install state>\doctor next to the installer's copy.
function Get-GpuRequirements {
    $file = @((Join-Path $PSScriptRoot '..\requirements.json'), (Join-Path $script:DoctorRepoDir 'scripts\install\requirements.json')) |
        Where-Object { Test-Path $_ } | Select-Object -First 1
    if (-not $file) { return $null }
    $components = @((Get-Content $file -Raw | ConvertFrom-Json).gpu.components)
    $top = $components | Sort-Object { [version]$_.driverMin } -Descending | Select-Object -First 1
    [pscustomobject]@{ Components = $components; Min = [version]$top.driverMin; By = "$($top.name)$(if ($top.PSObject.Properties.Name -contains 'cuda') { " (CUDA $($top.cuda))" })" }
}

function Get-NvidiaLatestDriver([string]$GpuName) {
    $ProgressPreference = 'SilentlyContinue'
    if (-not $script:NvidiaProducts) {
        [xml]$x = (Invoke-WebRequest $script:NvidiaLookup -UseBasicParsing -TimeoutSec 30).Content
        $script:NvidiaProducts = @($x.LookupValueSearch.LookupValues.LookupValue)
    }
    # Windows says "NVIDIA GeForce RTX 4090 Laptop GPU"; NVIDIA's list says "GeForce RTX 4090 Laptop GPU".
    $norm = { param($n) ($n -replace '^NVIDIA\s+', '').Trim().ToLowerInvariant() }
    $product = $script:NvidiaProducts | Where-Object { (& $norm $_.Name) -eq (& $norm $GpuName) } | Select-Object -First 1
    if (-not $product) { return @() }
    foreach ($c in @(@{ Label = 'Game Ready'; Query = '&upCRD=0&isWHQL=1' }, @{ Label = 'Studio'; Query = '&upCRD=1' })) {
        try {
            $j = (Invoke-WebRequest "$($script:NvidiaDriverService)&psid=$($product.ParentID)&pfid=$($product.Value)$($c.Query)" -UseBasicParsing -TimeoutSec 30).Content | ConvertFrom-Json
            if ([int]$j.Success -lt 1) { continue }
            $d = $j.IDS[0].downloadInfo
            [pscustomobject]@{ Channel = $c.Label; Version = [version]$d.Version; Released = $d.ReleaseDateTime; Url = $d.DownloadURL }
        } catch { [pscustomobject]@{ Channel = $c.Label; Version = $null; Released = ''; Url = "lookup failed ($($_.Exception.Message))" } }
    }
}

function Get-GpuDriverReport {
    $lines = [System.Collections.Generic.List[string]]::new()
    $adapters = @(Get-CimInstance Win32_VideoController -ErrorAction SilentlyContinue | Where-Object { $_.Name -notmatch 'Microsoft (Basic|Remote)|Virtual|Parsec|Meta' })
    $lines.Add('Display adapters (Windows):')
    foreach ($v in $adapters) {
        $lines.Add("  $($v.Name): driver $($v.DriverVersion), dated $(if ($v.DriverDate) { $v.DriverDate.ToString('yyyy-MM-dd') } else { 'unknown' })")
    }
    $other = @($adapters | Where-Object { $_.Name -notmatch 'NVIDIA' })
    $hasNvidia = [bool]($adapters | Where-Object { $_.Name -match 'NVIDIA' })
    $lines.Add("Hybrid graphics (NVIDIA plus another GPU, so Windows picks a GPU per app): $(if ($hasNvidia -and $other) { "YES, with $($other.Name -join ', ')" } else { 'no' })")
    $smi = Get-Command nvidia-smi -ErrorAction SilentlyContinue
    if (-not $smi) { $lines.Add('nvidia-smi: not found (no NVIDIA driver, or it is not on PATH)'); return $lines -join "`n" }
    $gpus = @(& $smi.Source --query-gpu=index,uuid,name,driver_version,memory.total,memory.used,pci.bus_id --format=csv,noheader,nounits 2>$null | ForEach-Object {
        $p = $_ -split ',\s*'; [pscustomobject]@{ Index = $p[0]; Uuid = $p[1]; Name = $p[2]; Driver = $p[3]; TotalMB = $p[4]; UsedMB = $p[5]; Bus = $p[6] } })
    $lines.Add("NVIDIA GPUs (nvidia-smi; all share one driver; the index can differ inside containers, the UUID does not): $($gpus.Count)")
    foreach ($g in $gpus) { $lines.Add("  GPU $($g.Index): $($g.Name), $($g.Uuid), driver $($g.Driver), $($g.UsedMB)/$($g.TotalMB) MB used, bus $($g.Bus)") }
    $installed = if ($gpus) { [version]$gpus[0].Driver } else { $null }
    $req = try { Get-GpuRequirements } catch { $null }
    if ($installed -and $req) {
        $lines.Add("Installed driver $installed $(if ($installed -ge $req.Min) { 'meets' } else { 'is OLDER than' }) the $($req.Min) minimum this stack needs (set by $($req.By)).")
    } elseif ($installed) { $lines.Add('The stack''s driver minimum is unknown (requirements.json not found).') }
    $lines.Add('Latest drivers from nvidia.com:')
    foreach ($name in @($gpus.Name | Sort-Object -Unique)) {
        $latest = @(try { Get-NvidiaLatestDriver $name } catch { [pscustomobject]@{ Channel = 'any'; Version = $null; Released = ''; Url = "lookup failed ($($_.Exception.Message))" } })
        if (-not $latest) { $lines.Add("  $($name): not in NVIDIA's product list, so the latest driver cannot be looked up"); continue }
        foreach ($l in $latest) {
            if (-not $l.Version) { $lines.Add("  $name, $($l.Channel): $($l.Url)"); continue }
            $state = if ($installed -and $l.Version -gt $installed) { "NEWER than installed $installed" } else { 'installed is up to date' }
            $lines.Add("  $name, $($l.Channel) $($l.Version) released $($l.Released): $state; $($l.Url)")
        }
    }
    if (Test-Path (Join-Path $script:DoctorRepoDir 'docker-compose.yml')) {
        $cfg = Invoke-Docker ((Get-RepoComposeArgs) + @('config', '--format', 'json'))
        if ($cfg.Code -eq 0) {
            $services = ($cfg.Out | ConvertFrom-Json).services
            $lines.Add('GPU reservations in the compose files:')
            foreach ($s in $services.PSObject.Properties) {
                $res = $s.Value.deploy.resources.reservations.devices | Where-Object { $_.capabilities -contains 'gpu' }
                foreach ($r in @($res)) { $lines.Add("  $($s.Name): $(if ($r.device_ids) { 'device_ids ' + ($r.device_ids -join ',') } elseif ($r.count) { "count $($r.count)" } else { 'all GPUs' })") }
            }
        }
        $lines.Add('GPU seen inside each running GPU service (end to end):')
        foreach ($c in @(if ($req) { $req.Components | Where-Object { $_.PSObject.Properties.Name -contains 'probe' } })) {
            $lines.Add("  $($c.name): $(Get-ContainerGpuProbe $c $gpus)")
        }
    }
    $lines -join "`n"
}

# Runs the fixed probes from requirements.json in an already running service
# container; never starts one. timeout keeps a hung CUDA init from blocking.
function Get-ContainerGpuProbe($Component, $HostGpus) {
    $c = Get-RepoComposeArgs
    $running = @((Invoke-Docker ($c + @('ps', '--status', 'running', '--services'))).Out -split "`r?`n" | ForEach-Object { $_.Trim() })
    if ($running -notcontains $Component.name) { return 'not running, so not probed' }
    $smi = Invoke-Docker ($c + @('exec', '-T', $Component.name, 'timeout', '30', 'nvidia-smi', '--query-gpu=uuid,name', '--format=csv,noheader'))
    if ($smi.Code -ne 0) { return "FAILED: nvidia-smi in the container exited $($smi.Code): $(Limit-Text $smi.Out.Trim() 300)" }
    $seen = @($smi.Out.Trim() -split "`r?`n" | Where-Object { $_ } | ForEach-Object {
        $uuid = ($_ -split ',\s*')[0]
        $hostGpu = $HostGpus | Where-Object { $_.Uuid -eq $uuid }
        "$_$(if ($hostGpu) { " (host GPU $($hostGpu.Index))" })"
    })
    # Windows PowerShell 5.1 drops embedded double quotes from native arguments,
    # so the probe travels base64-encoded and is decoded in the container.
    $b64 = [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($Component.probe))
    $probe = Invoke-Docker ($c + @('exec', '-T', $Component.name, 'timeout', '90', 'sh', '-c', "echo $b64 | base64 -d | sh"))
    $last = (@($probe.Out.Trim() -split "`r?`n") | Where-Object { $_ } | Select-Object -Last 1)
    "sees $($seen -join '; '); $($Component.what) probe $(if ($probe.Code -eq 0) { "OK: $last" } else { "FAILED (exit $($probe.Code)): $(Limit-Text "$last" 300)" })"
}

# GPU load while the stack runs, sampled once a second. Taken before a local
# doctor model loads, so the numbers are the stack's alone. Windows reports no
# per-process GPU memory (WDDM), and container processes are not named.
function Get-GpuLoadSnapshot([int]$Seconds = 10) {
    $smi = Get-Command nvidia-smi -ErrorAction SilentlyContinue
    if (-not $smi) { return 'nvidia-smi not found; GPU load not measured.' }
    $samples = for ($i = 0; $i -lt $Seconds; $i++) {
        & $smi.Source --query-gpu=index,name,utilization.gpu,memory.used,memory.total --format=csv,noheader,nounits 2>$null | ForEach-Object {
            $p = $_ -split ',\s*'; [pscustomobject]@{ Index = $p[0]; Name = $p[1]; Util = [int]$p[2]; UsedMB = [int]$p[3]; TotalMB = [int]$p[4] } }
        Start-Sleep -Seconds 1
    }
    $lines = [System.Collections.Generic.List[string]]::new()
    $lines.Add("GPU load over $Seconds s at $(Get-Date -Format 'HH:mm:ss'), before the doctor's own model was loaded:")
    foreach ($g in @($samples | Group-Object Index)) {
        $u = $g.Group | Measure-Object Util -Average -Maximum
        $m = $g.Group | Measure-Object UsedMB -Maximum
        $total = $g.Group[0].TotalMB
        $lines.Add("  GPU $($g.Name) ($($g.Group[0].Name)): utilization average $([math]::Round($u.Average))%, peak $($u.Maximum)%; memory used up to $($m.Maximum) of $total MB ($([math]::Round(100 * $m.Maximum / $total))%)")
    }
    $apps = @(& $smi.Source --query-compute-apps=process_name --format=csv,noheader 2>$null | ForEach-Object { $_.Trim() } | Where-Object { $_ })
    $named = @($apps | Where-Object { $_ -notmatch 'Insufficient Permissions' } | ForEach-Object { Split-Path $_ -Leaf } | Sort-Object -Unique)
    $lines.Add("  Programs using the GPU (Windows names only; containers are not listed): $(if ($named) { $named -join ', ' } else { 'none named' })$(if ($apps.Count -gt $named.Count) { "; $($apps.Count - $named.Count) more not visible to this account" })")
    $unity = @(Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object { $_.Name -match '\.exe$' -and $_.CommandLine -match '--ros-port' })
    $unityOnGpu = @($unity | Where-Object { $apps -match [regex]::Escape($_.Name) })
    $lines.Add("  Unity clients running: $($unity.Count)$(if ($unity) { ", $($unityOnGpu.Count) of them listed by nvidia-smi (on the NVIDIA GPU)" })")
    $out = Join-Path $script:DoctorRepoDir 'rl_agent\robotaxi.out'
    $train = if (Test-Path $out) { Select-String -Path $out -Pattern 'TRAIN end:\s+iter=' | Select-Object -Last 1 } else { $null }
    $lines.Add($(if ($train) { "  Trainer: last training step logged $([math]::Round(((Get-Date) - (Get-Item $out).LastWriteTime).TotalSeconds)) s ago: $($train.Line.Trim())" } else { '  Trainer: no training steps logged (rl_agent\robotaxi.out), so training was not running' }))
    $lines -join "`n"
}

$script:DoctorToolSchemas = @(
    @{ type = 'function'; function = @{ name = 'system_facts'; description = 'Snapshot of this PC: Windows, RAM, GPU/driver, disk, Docker Desktop (which sessions run it), docker info, WSL distros, Windows excluded port ranges, Unity clients and supervisor scripts running.'; parameters = @{ type = 'object'; properties = @{}; required = @() } } },
    @{ type = 'function'; function = @{ name = 'gpu_drivers'; description = 'Every display adapter with its driver version and date, each NVIDIA GPU (index, UUID, memory use, bus), the driver minimum this stack needs, the latest Game Ready and Studio drivers nvidia.com lists for each NVIDIA model, which compose services reserve GPUs (count or device_ids), and for each running GPU service which GPU it sees inside its container and whether its framework (TensorFlow, CuPy) can use it. Sends only the GPU model name to nvidia.com.'; parameters = @{ type = 'object'; properties = @{}; required = @() } } },
    @{ type = 'function'; function = @{ name = 'compose_ps'; description = 'All containers of the docker compose project, including stopped ones, with state and ports. Also lists the services defined in the compose files.'; parameters = @{ type = 'object'; properties = @{}; required = @() } } },
    @{ type = 'function'; function = @{ name = 'service_logs'; description = 'Recent log lines of one compose service (e.g. sim-controller, ros-server, mongo, dashboard, fly-brain).'; parameters = @{ type = 'object'; properties = @{ service = @{ type = 'string' }; lines = @{ type = 'integer'; description = 'default 150' }; filter = @{ type = 'string'; description = 'optional regex; only matching lines are returned' } }; required = @('service') } } },
    @{ type = 'function'; function = @{ name = 'read_file'; description = 'Read a text file. Relative paths are relative to the repo. .env is shown with secret values masked; other files holding secrets (keys, credentials) cannot be read.'; parameters = @{ type = 'object'; properties = @{ path = @{ type = 'string' }; start_line = @{ type = 'integer'; description = 'default 1' }; max_lines = @{ type = 'integer'; description = 'default 200, max 400' } }; required = @('path') } } },
    @{ type = 'function'; function = @{ name = 'list_dir'; description = 'List a directory (names, sizes, modified times). Relative paths are relative to the repo.'; parameters = @{ type = 'object'; properties = @{ path = @{ type = 'string' } }; required = @('path') } } },
    @{ type = 'function'; function = @{ name = 'grep'; description = 'Search files under a path for a regex. Returns path:line: text.'; parameters = @{ type = 'object'; properties = @{ pattern = @{ type = 'string' }; path = @{ type = 'string'; description = 'file or directory, default the repo' }; include = @{ type = 'string'; description = 'file name filter, e.g. *.yml' } }; required = @('pattern') } } },
    @{ type = 'function'; function = @{ name = 'run_command'; description = 'Run a read-only PowerShell command in the repo folder and return its output. Commands that could change anything are refused: use Get-*/Test-* cmdlets, docker ps/logs/inspect/images, docker compose ps/logs/config, git status/log/diff, netsh ... show, wsl -l -v, nvidia-smi, powercfg /getactivescheme, /list or /query, Invoke-RestMethod to localhost, and similar.'; parameters = @{ type = 'object'; properties = @{ command = @{ type = 'string' }; timeout_seconds = @{ type = 'integer'; description = 'default 60, max 180' } }; required = @('command') } } },
    @{ type = 'function'; function = @{ name = 'finish'; description = 'Report the diagnosis. Call once you have evidence for the root cause.'; parameters = @{ type = 'object'; properties = @{
        root_cause = @{ type = 'string'; description = 'The underlying condition, stated precisely (what is wrong, not the symptom).' }
        evidence = @{ type = 'string'; description = 'What you observed that shows it.' }
        fix = @{ type = 'string'; description = 'Exact steps or commands for the user to fix it.' }
        confidence = @{ type = 'string'; enum = @('high', 'medium', 'low') } }; required = @('root_cause', 'evidence', 'fix', 'confidence') } } }
)

function Invoke-DoctorTool([string]$Name, $A) { Protect-Secrets "$(Invoke-DoctorToolUnredacted $Name $A)" }

function Invoke-DoctorToolUnredacted([string]$Name, $A) {
    switch ($Name) {
        'system_facts' { return Get-SystemFacts }
        'gpu_drivers' { return Get-GpuDriverReport }
        'compose_ps' {
            $c = Get-RepoComposeArgs
            $ps = Invoke-Docker ($c + @('ps', '-a', '--format', 'table {{.Service}}\t{{.State}}\t{{.Status}}\t{{.Ports}}'))
            $svc = Invoke-Docker ($c + @('config', '--services'))
            $cmd = 'docker ' + (($c | ForEach-Object { if ($_ -match '\s') { "`"$_`"" } else { $_ } }) -join ' ')
            return "Containers:`n$($ps.Out.Trim())`n`nServices defined in the compose files:`n$(($svc.Out.Trim() -split "`r?`n") -join ', ')`n`nThis project's compose command (use it in fixes): $cmd"
        }
        'service_logs' {
            if (-not $A.service) { return 'error: service is required' }
            $n = if ($A.lines) { [math]::Min([int]$A.lines, 1000) } else { 150 }
            $c = Get-RepoComposeArgs
            $id = @((Invoke-Docker ($c + @('ps', '-a', '-q', "$($A.service)"))).Out.Trim() -split "`r?`n" | Where-Object { $_ })[0]
            if (-not $id) {
                $defined = @((Invoke-Docker ($c + @('config', '--services'))).Out -split "`r?`n" | ForEach-Object { $_.Trim() }) -contains "$($A.service)"
                return $(if ($defined) { "No logs: service '$($A.service)' is defined in the compose files but has no container (never created, or removed)." } else { "No logs: there is no service named '$($A.service)' in the compose files." })
            }
            $state = (Invoke-Docker @('inspect', '-f', '{{.State.Status}}|{{.State.StartedAt}}|{{.State.ExitCode}}', $id)).Out.Trim() -split '\|'
            $startedAt = if ($state.Count -ge 2 -and $state[1].Length -ge 19) { $state[1].Substring(0, 19) } else { '' }
            $r = Invoke-Docker ($c + @('logs', '--no-color', '--timestamps', '--tail', "$n", "$($A.service)"))
            # Logs span container restarts; mark where the current run begins.
            $marked = $false
            $lines = foreach ($l in (($r.Out -replace '\x1b\[[0-9;]*m', '') -split "`r?`n")) {
                if ($l -match '^(.*?\|\s*)(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)\.\d+Z\s?(.*)$') {
                    if (-not $marked -and $startedAt -and $Matches[2] -ge $startedAt) { "----- current container started $startedAt UTC; lines above are from an earlier run -----"; $marked = $true }
                    "$($Matches[1])$($Matches[2])Z $($Matches[3])"
                } else { $l }
            }
            $text = ($lines -join "`n").Trim()
            if (-not $text) { $text = '(the container exists but has written no log output)' }
            $text = "Container state: $($state[0]), started $startedAt UTC$(if ($state[0] -ne 'running') { ", exit code $($state[2])" })`n$text"
            if ($A.filter) { $text = (($text -split "`r?`n") | Where-Object { $_ -match $A.filter }) -join "`n"; if (-not $text) { $text = "(no lines match '$($A.filter)')" } }
            return "[exit $($r.Code)]`n$text"
        }
        'read_file' {
            $p = Resolve-DoctorPath $A.path
            if (Test-HiddenPath $p) { return "error: $p is part of the doctor itself, not the install" }
            if (-not (Test-Path -LiteralPath $p -PathType Leaf)) { return "error: no such file: $p" }
            if ($p -match $script:EnvFileRegex) { return Get-MaskedEnvFile $p }
            if (Test-SecretPath $p) { return "error: $p holds secrets (keys or credentials) and cannot be read" }
            if ((Get-Item -LiteralPath $p).Length -gt 20MB) { return 'error: file is larger than 20 MB; use grep' }
            $start = if ($A.start_line) { [math]::Max(1, [int]$A.start_line) } else { 1 }
            $max = if ($A.max_lines) { [math]::Min([int]$A.max_lines, 400) } else { 200 }
            $all = @(Get-Content -LiteralPath $p -Encoding UTF8)
            $slice = @($all | Select-Object -Skip ($start - 1) -First $max)
            $i = $start
            $body = ($slice | ForEach-Object { '{0,5}| {1}' -f $i++, $_ }) -join "`n"
            return "$p (lines $start-$($start + $slice.Count - 1) of $($all.Count))`n$body"
        }
        'list_dir' {
            $p = Resolve-DoctorPath $A.path
            if (Test-HiddenPath $p) { return "error: $p is part of the doctor itself, not the install" }
            if (-not (Test-Path -LiteralPath $p -PathType Container)) { return "error: no such directory: $p" }
            $items = @(Get-ChildItem -LiteralPath $p -Force -ErrorAction SilentlyContinue | Where-Object { -not (Test-HiddenPath $_.FullName) } | Select-Object -First 200)
            return "$p`n" + (($items | ForEach-Object { '{0,-4} {1,12} {2:yyyy-MM-dd HH:mm} {3}' -f $(if ($_.PSIsContainer) { 'dir' } else { '' }), $(if ($_.PSIsContainer) { '' } else { $_.Length }), $_.LastWriteTime, $_.Name }) -join "`n")
        }
        'grep' {
            if (-not $A.pattern) { return 'error: pattern is required' }
            $p = Resolve-DoctorPath $A.path
            if (Test-HiddenPath $p) { return "error: $p is part of the doctor itself, not the install" }
            if (Test-SecretPath $p) { return "error: $p holds secrets and cannot be searched; read_file shows .env with secret values masked" }
            $files = if (Test-Path -LiteralPath $p -PathType Leaf) { @(Get-Item -LiteralPath $p) } else {
                @(Get-ChildItem -LiteralPath $p -Recurse -File -Force -Filter $(if ($A.include) { $A.include } else { '*' }) -ErrorAction SilentlyContinue |
                    Where-Object { $_.FullName.Substring($p.TrimEnd('\').Length) -notmatch '\\(node_modules|\.git|Library|Temp|Logs|instances|__pycache__)\\' -and -not (Test-HiddenPath $_.FullName) -and -not (Test-SecretPath $_.FullName) -and $_.Length -lt 5MB -and $_.Extension -notmatch '^\.(dll|exe|pdb|png|jpg|gguf|zip|bin|assets|resS|resource)$' } | Select-Object -First 3000)
            }
            $hits = @($files | Select-String -Pattern $A.pattern -ErrorAction SilentlyContinue | Select-Object -First 60)
            if (-not $hits) { return "(no matches for '$($A.pattern)' under $p)" }
            return ($hits | ForEach-Object { "$($_.Path.Replace($script:DoctorRepoDir + '\', '')):$($_.LineNumber): $($_.Line.Trim())" }) -join "`n"
        }
        'run_command' {
            if (-not $A.command) { return 'error: command is required' }
            $why = Test-ReadOnlyCommand $A.command
            if ($why) { return "refused: $why. Diagnosis mode is read-only; put any change in finish.fix." }
            if (Test-HiddenPath $A.command) { return 'refused: that path is part of the doctor itself, not the install.' }
            $t = if ($A.timeout_seconds) { [math]::Min([int]$A.timeout_seconds, 180) } else { 60 }
            return Remove-HiddenLines (Invoke-IsolatedPowerShell $A.command $t)
        }
        default { return "error: unknown tool '$Name'. Tools: $($script:DoctorToolSchemas.function.name -join ', ')" }
    }
}
