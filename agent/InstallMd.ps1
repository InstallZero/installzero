# Reads install.md files (format in INSTALL-MD.md), describes this machine,
# and picks the blocks that apply to it.

function Read-InstallMd([string]$Path) {
    $lines = @(Get-Content -LiteralPath $Path -Encoding UTF8)
    $meta = [ordered]@{}
    $i = 0
    if ($lines.Count -and $lines[0].Trim() -eq '---') {
        for ($i = 1; $i -lt $lines.Count -and $lines[$i].Trim() -ne '---'; $i++) {
            if ($lines[$i] -match '^\s*([\w-]+)\s*:\s*(.*)$') { $meta[$Matches[1].ToLowerInvariant()] = $Matches[2].Trim() }
        }
        $i++
    }
    $steps = [System.Collections.Generic.List[object]]::new()
    $step = $null; $block = $null
    $prose = [System.Collections.Generic.List[string]]::new()
    for (; $i -lt $lines.Count; $i++) {
        $line = $lines[$i]
        if ($block) {
            if ($line -match '^```\s*$') { $step.Blocks.Add($block); $block = $null } else { $block.Lines.Add($line) }
            continue
        }
        $heading = $null
        if ($line -match '^##\s+Step\s+(\d+)\s*:\s*([a-z0-9][a-z0-9.-]*)\s*(?:[-:]+\s*(.*))?$') { $heading = @([int]$Matches[1], $Matches[2], "$($Matches[3])".Trim()) }
        # Without an id ("## Step 3: Clone the repository") the id comes from the title.
        elseif ($line -match '^##\s+Step\s+(\d+)\s*[:.)-]\s*(.+)$') { $heading = @([int]$Matches[1], (($Matches[2].ToLowerInvariant() -replace '[^a-z0-9]+', '-').Trim('-')), $Matches[2].Trim()) }
        if ($heading) {
            $step = [pscustomobject]@{ Number = $heading[0]; Id = $heading[1]; Title = $heading[2]; Why = ''; Requires = [ordered]@{}; Blocks = [System.Collections.Generic.List[object]]::new(); Line = $i + 1 }
            $steps.Add($step)
            continue
        }
        if ($step -and $line -match '^```(\w+)\s*(.*)$') {
            $kind = $Matches[1].ToLowerInvariant()
            $words = @($Matches[2].Trim() -split '\s+' | Where-Object { $_ })
            $shell = @($words | Where-Object { $_ -notmatch '=' }) | Select-Object -First 1
            $when = [ordered]@{}
            foreach ($w in @($words | Where-Object { $_ -match '=' })) { $k, $v = $w -split '=', 2; $when[$k.ToLowerInvariant()] = $v.ToLowerInvariant() }
            $block = [pscustomobject]@{ Kind = $kind; Shell = $(if ($shell) { $shell.ToLowerInvariant() } else { '' }); When = $when; Lines = [System.Collections.Generic.List[string]]::new() }
            continue
        }
        if ($step -and -not $step.Why -and $line -match '^\s*Why:\s*(.+)$') { $step.Why = $Matches[1].Trim(); continue }
        if (-not $step) { $prose.Add($line) }
    }
    foreach ($s in $steps) {
        foreach ($b in $s.Blocks) { $b | Add-Member -NotePropertyName Code -NotePropertyValue (($b.Lines -join "`n").Trim()) }
        foreach ($r in @($s.Blocks | Where-Object Kind -eq 'requires')) {
            foreach ($l in ($r.Code -split "`n")) { if ($l -match '^\s*([\w-]+)\s*:\s*(.+?)\s*(#.*)?$') { $s.Requires[$Matches[1].ToLowerInvariant()] = $Matches[2].Trim().ToLowerInvariant() } }
        }
    }
    [pscustomobject]@{ Path = (Resolve-Path -LiteralPath $Path).Path; Meta = $meta; Steps = $steps.ToArray(); Prose = ($prose -join "`n").Trim(); Text = ($lines -join "`n") }
}

# Problems that make a document unusable as structured steps; empty when fine.
function Test-InstallMd($Doc) {
    $problems = [System.Collections.Generic.List[string]]::new()
    if (-not $Doc.Meta['name']) { $problems.Add('front matter has no name:') }
    if (-not $Doc.Steps.Count) { $problems.Add('no "## Step <n>: <id> - <title>" headings') }
    $seen = @{}
    foreach ($s in $Doc.Steps) {
        if ($seen[$s.Id]) { $problems.Add("step id '$($s.Id)' is used twice") }; $seen[$s.Id] = $true
        $kinds = @($s.Blocks.Kind)
        if ($kinds -notcontains 'verify') { $problems.Add("step $($s.Number) ($($s.Id)) has no verify block") }
        if ($kinds -notcontains 'install' -and $kinds -notcontains 'detect') { $problems.Add("step $($s.Number) ($($s.Id)) has neither install nor detect") }
        foreach ($b in $s.Blocks) {
            if ($b.Kind -notin 'requires', 'detect', 'install', 'verify') { $problems.Add("step $($s.Number) ($($s.Id)): unknown block kind '$($b.Kind)'") }
            if ($b.Kind -ne 'requires' -and $b.Shell -and $b.Shell -notin 'powershell', 'pwsh', 'bash', 'sh') { $problems.Add("step $($s.Number) ($($s.Id)): unknown shell '$($b.Shell)'") }
            if ($b.Kind -ne 'requires' -and -not $b.Code) { $problems.Add("step $($s.Number) ($($s.Id)): empty $($b.Kind) block") }
            if ($b.Kind -in 'detect', 'verify' -and $b.Code -match '(?m)^\s*exit\s+0\s*\z' -and $b.Code -notmatch '\b(if|throw|exit\s+[1-9])\b') {
                $problems.Add("step $($s.Number) ($($s.Id)): $($b.Kind) ends with exit 0 and has no failing path, so it cannot fail; exit non-zero when the check does not hold")
            }
        }
    }
    $problems.ToArray()
}

function Get-MachineProfile([string]$InstallDir = $env:USERPROFILE) {
    $os = if ($IsLinux) { 'linux' } elseif ($IsMacOS) { 'macos' } else { 'windows' }
    $arch = switch ($env:PROCESSOR_ARCHITECTURE) { 'AMD64' { 'x64' } 'ARM64' { 'arm64' } default { "$($env:PROCESSOR_ARCHITECTURE)".ToLowerInvariant() } }
    $adapters = @(Get-CimInstance Win32_VideoController -ErrorAction SilentlyContinue | Where-Object { $_.Name -notmatch 'Microsoft (Basic|Remote)|Virtual|Parsec|Meta' }).Name
    $gpus = @($adapters | ForEach-Object { switch -Regex ($_) { 'NVIDIA' { 'nvidia' } 'AMD|Radeon' { 'amd' } 'Intel' { 'intel' } default { 'other' } } })
    $gpu = foreach ($v in 'nvidia', 'amd', 'intel', 'other') { if ($gpus -contains $v) { $v; break } }
    $driver = $null
    if ($gpus -contains 'nvidia' -and (Get-Command nvidia-smi -ErrorAction SilentlyContinue)) { $driver = "$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>$null | Select-Object -First 1)".Trim() }
    $drive = Get-PSDrive ((Split-Path -Qualifier $InstallDir).TrimEnd(':')) -PSProvider FileSystem -ErrorAction SilentlyContinue
    [pscustomobject]@{
        os = $os; arch = $arch; gpu = $(if ($gpu) { $gpu } else { 'none' }); gpus = ($adapters -join ', ')
        ram_gb = [math]::Round((Get-CimInstance Win32_ComputerSystem).TotalPhysicalMemory / 1GB)
        disk_gb = $(if ($drive) { [math]::Round($drive.Free / 1GB) } else { $null })
        driver = $driver
        os_version = "$((Get-CimInstance Win32_OperatingSystem).Caption) $([Environment]::OSVersion.Version)"
    }
}

function Test-Condition([string]$Key, [string]$Want, $Machine) {
    $values = @($Want -split ',' | ForEach-Object { $_.Trim().ToLowerInvariant() } | Where-Object { $_ })
    switch ($Key) {
        { $_ -in 'os', 'arch' } { return $values -contains $Machine.$Key }
        'gpu' { return ($values -contains 'any') -or ($values -contains $Machine.gpu) }
        'min_ram_gb' { return $Machine.ram_gb -ge [double]$values[0] }
        'min_disk_gb' { return ($null -eq $Machine.disk_gb) -or $Machine.disk_gb -ge [double]$values[0] }
        'min_driver' { return $Machine.driver -and ([version]$Machine.driver -ge [version]$values[0]) }
        default { return $true }
    }
}

# Unmet requirements of a step, as readable strings; empty when it applies.
function Get-UnmetRequirements($Step, $Machine) {
    @(foreach ($k in $Step.Requires.Keys) {
        if (-not (Test-Condition $k $Step.Requires[$k] $Machine)) { "$k needs $($Step.Requires[$k]), this machine has $(switch ($k) { 'min_ram_gb' { "$($Machine.ram_gb) GB" } 'min_disk_gb' { "$($Machine.disk_gb) GB free" } 'min_driver' { $Machine.driver } default { $Machine.$k } })" }
    })
}

function Select-StepBlock($Step, [string]$Kind, $Machine) {
    $default = if ($Machine.os -eq 'windows') { 'powershell' } else { 'bash' }
    $fits = @($Step.Blocks | Where-Object {
        $b = $_
        $b.Kind -eq $Kind -and -not @($b.When.Keys | Where-Object { -not (Test-Condition $_ $b.When[$_] $Machine) }).Count -and
        ($Machine.os -ne 'windows' -or ($(if ($b.Shell) { $b.Shell } else { $default }) -in 'powershell', 'pwsh') -or $b.When['os'] -eq 'windows')
    })
    $fits | Sort-Object { $_.When.Count } -Descending | Select-Object -First 1
}

function Expand-Placeholders([string]$Code, [hashtable]$Values) {
    foreach ($k in $Values.Keys) { $Code = $Code.Replace("{{$k}}", $Values[$k]) }
    $Code
}
