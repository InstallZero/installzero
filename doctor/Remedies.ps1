# Settings the optimize review measures in code rather than leaving to the
# model: each check reports the current value, the target, whether a change
# is needed, the manual steps, and (when it is safe without elevation and
# without stopping the running stack) an apply action. apply_change re-runs
# the check first, so a change is never applied when it is not needed.
# Requires Tools.ps1 (Get-GpuRequirements, Get-NvidiaLatestDriver).

$script:PowerPlans = @{
    '381b4222-f694-41f0-9685-ff5bb260df2e' = 'Balanced'
    '8c5e7fda-e8bf-4a96-9a85-a6e23a8c635c' = 'High performance'
    'e9a42b02-d5df-448d-aa00-03f14749eb61' = 'Ultimate performance'
    'a1841308-3541-4fab-bc81-f71556f20b4a' = 'Power saver'
}
# Windows 11 Settings > Power mode, stored as an overlay on the Balanced plan.
$script:PowerModes = @{
    '00000000-0000-0000-0000-000000000000' = 'Balanced'
    '961cc777-2547-4f9d-8174-7d86181b8a7a' = 'Best power efficiency'
    '3af9b8d9-7c97-431d-ad78-34a8bfea439f' = 'Better battery'
    'ded574b5-45a0-4f42-8737-46345c09c238' = 'Best performance'
}
$script:BestPerformanceMode = 'ded574b5-45a0-4f42-8737-46345c09c238'

function Initialize-PowerApi {
    if ('RlDoctor.Power' -as [type]) { return }
    Add-Type -Namespace RlDoctor -Name Power -MemberDefinition @'
[DllImport("powrprof.dll")] public static extern uint PowerSetActiveOverlayScheme(Guid overlay);
[DllImport("powrprof.dll")] public static extern uint PowerGetActualOverlayScheme(out Guid overlay);
'@
}

function New-Check([string]$Item, [string]$Status, [string]$Current, [string]$Target, [string]$Steps = '', [string]$Improves = '', [string]$Apply = '') {
    [pscustomobject]@{ Item = $Item; Status = $Status; Current = $Current; Target = $Target; Steps = $Steps; Improves = $Improves; Apply = $Apply }
}

function Get-PowerState {
    Initialize-PowerApi
    $plan = if ("$(powercfg /getactivescheme)" -match '([0-9a-f]{8}-[0-9a-f-]{27})\s+\((.+)\)') { @{ Guid = $Matches[1]; Name = $Matches[2] } } else { $null }
    $g = [guid]::Empty
    $mode = if ([RlDoctor.Power]::PowerGetActualOverlayScheme([ref]$g) -eq 0) { "$g" } else { $null }
    $battery = Get-CimInstance Win32_Battery -ErrorAction SilentlyContinue | Select-Object -First 1
    [pscustomobject]@{ Plan = $plan; Mode = $mode; OnBattery = ($battery -and $battery.BatteryStatus -eq 1) }
}

function Test-PowerSetting {
    $s = Get-PowerState
    $improves = 'the CPU and GPU hold higher clocks instead of slowing down to save power, so more simulation and training steps per hour.'
    if (-not $s.Plan) { return New-Check 'power' 'UNKNOWN' 'powercfg did not report the active plan' 'High performance plan, or Balanced with Power mode Best performance' }
    $planName = if ($script:PowerPlans[$s.Plan.Guid]) { $script:PowerPlans[$s.Plan.Guid] } else { "custom plan '$($s.Plan.Name)'" }
    $modeName = if ($s.Mode -and $script:PowerModes[$s.Mode]) { $script:PowerModes[$s.Mode] } elseif ($s.Mode) { $s.Mode } else { 'unknown' }
    $current = "plan $planName" + $(if ($planName -eq 'Balanced') { ", Power mode $modeName" }) + $(if ($s.OnBattery) { ', on battery' } else { '' })
    $target = 'High or Ultimate performance plan, or Balanced with Power mode Best performance, plugged in'
    if ($s.OnBattery) {
        return New-Check 'power' 'CHANGE' $current $target 'Plug the laptop in and keep it plugged in while training.' 'on battery a laptop slows down hard, and the run stops when the battery runs out.'
    }
    switch ($planName) {
        { $_ -in 'High performance', 'Ultimate performance' } { return New-Check 'power' 'OK' $current $target }
        'Balanced' {
            if ($s.Mode -eq $script:BestPerformanceMode) { return New-Check 'power' 'OK' $current $target }
            return New-Check 'power' 'CHANGE' $current $target 'Settings > System > Power & battery > Power mode: Best performance.' $improves 'set Power mode to Best performance (keeps the Balanced plan and its sleep settings)'
        }
        'Power saver' { return New-Check 'power' 'CHANGE' $current $target 'Settings > System > Power & battery: switch to the Balanced plan, then Power mode: Best performance.' $improves 'switch to the Balanced plan with Power mode Best performance' }
        default { return New-Check 'power' 'UNKNOWN' $current $target 'A custom (often manufacturer) plan; check its performance setting in the manufacturer''s tool.' }
    }
}

function Invoke-PowerChange {
    Initialize-PowerApi
    $s = Get-PowerState
    if ($s.Plan -and $script:PowerPlans[$s.Plan.Guid] -eq 'Power saver') {
        powercfg /setactive SCHEME_BALANCED | Out-Null
        if ($LASTEXITCODE -ne 0) { throw "powercfg /setactive SCHEME_BALANCED failed (exit $LASTEXITCODE)" }
    }
    $rc = [RlDoctor.Power]::PowerSetActiveOverlayScheme([guid]$script:BestPerformanceMode)
    if ($rc -ne 0) { throw "Windows refused to set Power mode Best performance (error $rc)" }
}

function Get-SleepSeconds([string]$Setting) {
    $line = powercfg /query SCHEME_CURRENT SUB_SLEEP $Setting 2>$null | Select-String 'Current AC Power Setting Index'
    if ("$line" -match '0x([0-9a-f]+)') { [Convert]::ToInt64($Matches[1], 16) } else { $null }
}

function Test-SleepSetting {
    $standby = Get-SleepSeconds 'STANDBYIDLE'
    $hibernate = Get-SleepSeconds 'HIBERNATEIDLE'
    $fmt = { param($s) if ($null -eq $s) { 'unknown' } elseif ($s -eq 0) { 'never' } else { "after $([math]::Round($s / 60)) min" } }
    $current = "plugged in: sleep $(& $fmt $standby), hibernate $(& $fmt $hibernate)"
    $target = 'never sleep or hibernate while plugged in'
    if ($null -eq $standby) { return New-Check 'sleep' 'UNKNOWN' $current $target }
    if ($standby -eq 0 -and ($null -eq $hibernate -or $hibernate -eq 0)) { return New-Check 'sleep' 'OK' $current $target }
    New-Check 'sleep' 'CHANGE' $current $target 'Settings > System > Power & battery > Screen, sleep & hibernate timeouts: "When plugged in, put my device to sleep after" Never (and hibernate Never).' 'unattended and overnight runs keep going instead of stopping when the PC sleeps after a period without keyboard or mouse input.' 'set sleep and hibernate to Never when plugged in (the screen may still turn off)'
}

function Invoke-SleepChange {
    powercfg /change standby-timeout-ac 0 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "powercfg /change standby-timeout-ac 0 failed (exit $LASTEXITCODE)" }
    if ((Get-SleepSeconds 'HIBERNATEIDLE') -gt 0) { powercfg /change hibernate-timeout-ac 0 | Out-Null }
}

function Test-GpuDriverSetting {
    $target = 'the newest driver nvidia.com offers for this GPU'
    $smi = Get-Command nvidia-smi -ErrorAction SilentlyContinue
    if (-not $smi) { return New-Check 'gpu-driver' 'OK' 'no NVIDIA GPU (nvidia-smi not found)' $target }
    $gpu = @(& $smi.Source --query-gpu=name,driver_version --format=csv,noheader 2>$null | ForEach-Object { $p = $_ -split ',\s*'; [pscustomobject]@{ Name = $p[0]; Driver = [version]$p[1] } })
    if (-not $gpu) { return New-Check 'gpu-driver' 'UNKNOWN' 'nvidia-smi reported no GPU' $target }
    $installed = $gpu[0].Driver
    $req = try { Get-GpuRequirements } catch { $null }
    $latest = @(foreach ($n in @($gpu.Name | Sort-Object -Unique)) { try { Get-NvidiaLatestDriver $n } catch { } }) | Where-Object { $_.Version }
    $current = "installed $installed" + $(if ($req) { " (stack minimum $($req.Min))" })
    if ($req -and $installed -lt $req.Min) {
        $newest = $latest | Sort-Object Version -Descending | Select-Object -First 1
        $where = if ($newest) { $newest.Url } else { 'https://www.nvidia.com/Download/index.aspx' }
        return New-Check 'gpu-driver' 'CHANGE' "$current; REQUIRED update" $target "Install the NVIDIA driver from $where (restart Windows if the installer asks), then restart the stack." "$($req.By) can use the GPU at all (it needs driver $($req.Min) or newer)."
    }
    if (-not $latest) { return New-Check 'gpu-driver' 'UNKNOWN' "$current; nvidia.com lookup failed or this GPU is not in its list" $target }
    $newestVersion = ($latest | Measure-Object -Property Version -Maximum).Maximum
    $current += "; newest on nvidia.com $newestVersion"
    if ($installed -ge $newestVersion) { return New-Check 'gpu-driver' 'OK' "$current; installed is the newest" $target }
    $pick = $latest | Where-Object { $_.Version -eq $newestVersion } | Sort-Object { $_.Channel -ne 'Studio' } | Select-Object -First 1
    New-Check 'gpu-driver' 'CHANGE' "$current; recommended, not required (installed meets the minimum)" $target "Install the $($pick.Channel) driver $($pick.Version) from $($pick.Url) when no training runs (the display blinks and running containers may lose the GPU). Restart Windows only if the installer asks; then restart the stack so the containers load the new driver." "NVIDIA's latest fixes for CUDA and GPU use under WSL 2, which the containers rely on."
}

function Get-UnityClientExes {
    $builds = Join-Path $script:DoctorRepoDir 'unity\Builds'
    $exe = Get-ChildItem (Join-Path $builds 'latest') -Filter '*.exe' -File -ErrorAction SilentlyContinue | Where-Object { $_.Name -notlike 'UnityCrashHandler*' } | Select-Object -First 1
    if (-not $exe) { return @() }
    # instances\0..3 are the copies extra clients run from (RunNClients.ps1);
    # the preference is per exact path, so copies made later need it too.
    @($exe.FullName) + @(0..3 | ForEach-Object { Join-Path $builds "instances\$_\$($exe.Name)" })
}

function Test-UnityGpuSetting {
    $target = 'High performance (the NVIDIA GPU) for every Unity client exe'
    $adapters = @(Get-CimInstance Win32_VideoController -ErrorAction SilentlyContinue | Where-Object { $_.Name -notmatch 'Microsoft (Basic|Remote)|Virtual|Parsec|Meta' })
    if (-not ($adapters | Where-Object { $_.Name -match 'NVIDIA' }) -or -not ($adapters | Where-Object { $_.Name -notmatch 'NVIDIA' })) {
        return New-Check 'unity-gpu' 'OK' 'one GPU, so Windows has no choice to make' $target
    }
    $exes = @(Get-UnityClientExes)
    if (-not $exes) { return New-Check 'unity-gpu' 'UNKNOWN' 'no Unity client under unity\Builds\latest' $target }
    $prefs = Get-ItemProperty 'HKCU:\Software\Microsoft\DirectX\UserGpuPreferences' -ErrorAction SilentlyContinue
    $missing = @($exes | Where-Object { -not $prefs -or "$($prefs.$_)" -notmatch 'GpuPreference=2;' })
    $current = "hybrid graphics; $($exes.Count - $missing.Count) of $($exes.Count) client exe paths set to High performance"
    if (-not $missing) { return New-Check 'unity-gpu' 'OK' $current $target }
    # A driver profile can already route the clients to the NVIDIA GPU.
    $running = @(Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -and $exes -contains $_.Path })
    $onNvidia = @(nvidia-smi --query-compute-apps=process_name --format=csv,noheader 2>$null | ForEach-Object { $_.Trim() })
    if ($running -and -not @($running | Where-Object { $onNvidia -notcontains $_.Path })) {
        return New-Check 'unity-gpu' 'OK' "$current, but all $($running.Count) running clients already use the NVIDIA GPU (nvidia-smi)" $target
    }
    New-Check 'unity-gpu' 'CHANGE' $current $target "Settings > System > Display > Graphics: add each client exe and choose High performance:`n$(($missing | ForEach-Object { "     $_" }) -join "`n")" 'Unity renders on the NVIDIA GPU instead of the much slower integrated one, so the simulator produces more environment steps per second.' 'set High performance for the Unity client exes (takes effect when each client next starts)'
}

function Invoke-UnityGpuChange {
    $key = 'HKCU:\Software\Microsoft\DirectX\UserGpuPreferences'
    if (-not (Test-Path $key)) { New-Item $key -Force | Out-Null }
    foreach ($exe in Get-UnityClientExes) { New-ItemProperty $key -Name $exe -Value 'GpuPreference=2;' -PropertyType String -Force | Out-Null }
}

function Test-DiskSetting {
    $drives = @(@($env:LOCALAPPDATA, $script:DoctorRepoDir) | ForEach-Object { (Split-Path -Qualifier $_).TrimEnd(':') } | Sort-Object -Unique)
    $free = @($drives | ForEach-Object { $d = Get-PSDrive $_ -PSProvider FileSystem -ErrorAction SilentlyContinue; if ($d) { [pscustomobject]@{ Name = $_; GB = [math]::Round($d.Free / 1GB) } } })
    $current = ($free | ForEach-Object { "$($_.Name): $($_.GB) GB free" }) -join ', '
    $low = @($free | Where-Object { $_.GB -lt 50 })
    if (-not $free) { return New-Check 'disk' 'UNKNOWN' 'free space not readable' '50 GB free' }
    if (-not $low) { return New-Check 'disk' 'OK' $current '50 GB free on the Docker and repo drives' }
    New-Check 'disk' 'CHANGE' $current '50 GB free on the Docker and repo drives' "Free space on $(($low.Name | ForEach-Object { "$($_):" }) -join ', ') (Settings > System > Storage > Cleanup recommendations; docker system prune for unused images)." 'image rebuilds, checkpoints, logs and the database have room to grow; a full disk stops Docker and the trainer mid-run and can damage the MongoDB data.'
}

$script:SettingChecks = [ordered]@{
    'power'      = @{ Test = { Test-PowerSetting };     Apply = { Invoke-PowerChange } }
    'sleep'      = @{ Test = { Test-SleepSetting };     Apply = { Invoke-SleepChange } }
    'gpu-driver' = @{ Test = { Test-GpuDriverSetting }; Apply = $null }
    'disk'       = @{ Test = { Test-DiskSetting };      Apply = $null }
    'unity-gpu'  = @{ Test = { Test-UnityGpuSetting };  Apply = { Invoke-UnityGpuChange } }
}

function Get-SettingCheck([string]$Item) {
    try { & $script:SettingChecks[$Item].Test } catch { New-Check $Item 'UNKNOWN' "check failed: $_" '' }
}

# Re-checks, applies only when still needed, and measures again. $Confirm is
# a scriptblock taking the check and returning $true to go ahead.
function Invoke-SettingChange([string]$Item, [scriptblock]$Confirm) {
    if (-not $script:SettingChecks.Contains($Item)) { return "error: unknown item '$Item'; known: $($script:SettingChecks.Keys -join ', ')" }
    $before = Get-SettingCheck $Item
    if ($before.Status -ne 'CHANGE') { return "not applied: not needed. $Item is $($before.Status): $($before.Current)" }
    if (-not $script:SettingChecks[$Item].Apply -or -not $before.Apply) { return "not applied: $Item cannot be changed automatically. Manual steps: $($before.Steps)" }
    if (-not (& $Confirm $before)) { return "not applied: the user declined. Current: $($before.Current)" }
    try { & $script:SettingChecks[$Item].Apply } catch { return "failed: $_. Current: $((Get-SettingCheck $Item).Current)" }
    $after = Get-SettingCheck $Item
    $out = "applied ($($before.Apply)). Before: $($before.Current). Now: $($after.Current) - $($after.Status)."
    if ($Item -eq 'power') {
        $sleep = Get-SettingCheck 'sleep'
        $out += " Sleep after the power change: $($sleep.Current) - $($sleep.Status)."
    }
    $out
}
