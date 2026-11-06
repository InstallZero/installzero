# Read-only policy for PowerShell commands the doctor and the Windows probe
# run: Test-ReadOnlyCommand returns $null when a command is allowed, otherwise
# why not. Self-contained so the Go engine can embed and run this same file.

# Names whose values are secrets (API_KEY, ..._TOKEN_SECRET, SMTP_PASSWORD);
# not ..._TOKEN_TTL_SECONDS.
$script:SecretNameRegex = '(?i)(^|_)(API_?KEY|KEY|SECRET|TOKEN|PASSWORD|PASSWD|PWD|PASS|CREDENTIALS?|COOKIE|AUTH)$'
function Test-SecretName([string]$Name) { $Name -match $script:SecretNameRegex }
# A secret file named in a command, and the env: drive used as a path
# (Get-ChildItem env:). $env:NAME is checked by name in Test-ReadOnlyCommand.
$script:SecretFileInCommandRegex = '(?i)((^|[\s''"\\/(=,])\.env(?!\.(example|sample|template)\b)(\b|$)|(^|[\s''"\\/(=,])secrets[\\/]|\.(pem|pfx|p12)\b|id_(rsa|dsa|ecdsa|ed25519)\b|\.ssh[\\/]|\.git-credentials|\.(netrc|npmrc|pypirc)\b|\.aws[\\/]credentials|\.docker[\\/]config\.json)'
$script:EnvDriveRegex = '(?i)(?<![\$\w{])env:'

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
