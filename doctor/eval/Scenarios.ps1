# Fault-injection scenarios for evaluating the install doctor.
#
# Each one recreates a failure we actually hit while testing the installer
# (see docs/install-doctor-plan.md). Dot-source this file; it defines
# $DoctorScenarios. Scriptblocks run with $RepoDir (the installed
# rl-roboracer clone) in scope and Invoke-RepoCompose available.
#
#   Id         stable name
#   Source     the incident it comes from
#   Requires   'admin' or 'second-account' when it cannot run unattended
#   Inject     breaks the install
#   Symptom    what the doctor is told, worded the way a user or the
#              installer would report it (never the cause)
#   RootCause  grading: every inner array must have at least one regex that
#              matches the doctor's root-cause text
#   Healthy    returns $true once the failure is gone
#   Restore    undoes Inject when the doctor did not

. (Join-Path $PSScriptRoot '..\Tools.ps1')

function Invoke-RepoCompose([string[]]$ComposeArgs) {
    Invoke-Docker ((Get-RepoComposeArgs $RepoDir) + $ComposeArgs) $RepoDir
}

# Files a scenario removes are parked outside the repo, so the doctor sees a
# plain missing file rather than a tell-tale renamed one.
$script:StashDir = Join-Path $env:LOCALAPPDATA 'rl-roboracer\eval-stash'

function Get-StashPath([string]$File) {
    $hash = [BitConverter]::ToString([Security.Cryptography.SHA1]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($File.ToLowerInvariant()))).Replace('-', '').Substring(0, 12)
    Join-Path $script:StashDir "$hash-$(Split-Path $File -Leaf)"
}

function Hide-File([string]$File) {
    if (-not (Test-Path $File)) { return }
    New-Item -ItemType Directory -Force $script:StashDir | Out-Null
    Move-Item $File (Get-StashPath $File) -Force
}

function Restore-File([string]$File) {
    $stash = Get-StashPath $File
    if ((Test-Path $stash) -and -not (Test-Path $File)) { Move-Item $stash $File -Force }
    # earlier versions renamed in place
    if ((Test-Path "$File.doctor-eval") -and -not (Test-Path $File)) { Move-Item "$File.doctor-eval" $File -Force }
}

function Test-ServiceRunning([string]$Service) {
    $r = Invoke-RepoCompose @('ps', '--status', 'running', '--services')
    @($r.Out -split "`r?`n" | ForEach-Object { $_.Trim() }) -contains $Service
}

function Test-InContainerPort([string]$Service, [int]$Port) {
    (Invoke-RepoCompose @('exec', '-T', $Service, 'python', '-c', "import socket; socket.create_connection(('127.0.0.1', $Port), 2)")).Code -eq 0
}

function Get-UnityClients {
    $builds = Join-Path $RepoDir 'unity\Builds'
    @(Get-Process -ErrorAction SilentlyContinue | Where-Object { $_.Path -and $_.Path.StartsWith($builds, [StringComparison]::OrdinalIgnoreCase) -and $_.Path -notmatch 'UnityCrashHandler' })
}

function Stop-UnityStack {
    Get-CimInstance Win32_Process -Filter "Name='powershell.exe' OR Name='pwsh.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -match 'RunClientWrapper|Start-ClientAtLogon' -and $_.CommandLine -like "*$RepoDir*" } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    Get-UnityClients | Stop-Process -Force -ErrorAction SilentlyContinue
}

function Start-UnityLauncher {
    $launcher = Join-Path $RepoDir 'scripts\install\Start-ClientAtLogon.ps1'
    $n = if ($Clients) { $Clients } else { 1 }
    Start-Process (Join-Path $PSHOME 'powershell.exe') -WindowStyle Minimized -ArgumentList '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$launcher`"", '-N', "$n"
}

$DoctorScenarios = @(
    @{
        Id = 'fly-brain-stopped'
        Source = 'fly-brain service never started on a fresh install (DNS resolution failed for fly-brain:50061)'
        Inject = { [void](Invoke-RepoCompose @('rm', '-sf', 'fly-brain')) }
        Symptom = 'A TRAIN job on the fly_donut_flow course fails immediately with: _InactiveRpcError: StatusCode.UNAVAILABLE, details = "DNS resolution failed for fly-brain:50061: C-ares status is not ARES_SUCCESS qtype=A name=fly-brain: Domain name not found"'
        RootCause = @(@('fly-?brain'), @('not running|isn''t running|stopped|no container|container (is )?(missing|absent|removed)|not (been )?(started|created)|never (started|created)|removed|is down|not among the running'))
        Healthy = { (Test-ServiceRunning 'fly-brain') -and (Test-InContainerPort 'fly-brain' 50061) }
        Restore = { [void](Invoke-RepoCompose @('up', '-d', '--no-build', 'fly-brain')) }
    },
    @{
        Id = 'fly-brain-stubs-broken'
        Source = 'generated gRPC stubs missing on a fresh clone (docker/fly_brain/gen is git-ignored)'
        Inject = {
            Hide-File (Join-Path $RepoDir 'docker\fly_brain\gen\fly_brain\proto\fly_brain_pb2_grpc.py')
            [void](Invoke-RepoCompose @('restart', 'fly-brain'))
        }
        Symptom = 'The fly-brain container keeps restarting, and fly courses fail to connect to fly-brain:50061.'
        RootCause = @(@('stub|pb2_grpc|generated|gen_protos|grpc code|protobuf'), @('missing|import|not found|ModuleNotFound|deleted'))
        Healthy = { (Test-ServiceRunning 'fly-brain') -and (Test-InContainerPort 'fly-brain' 50061) }
        Restore = {
            Restore-File (Join-Path $RepoDir 'docker\fly_brain\gen\fly_brain\proto\fly_brain_pb2_grpc.py')
            [void](Invoke-RepoCompose @('restart', 'fly-brain'))
        }
    },
    @{
        Id = 'mongo-folder-owner'
        Source = 'mkdir /bitnami/mongodb/data: Permission denied (uid 1001 cannot write a root-owned folder)'
        Requires = 'test-install'
        Inject = {
            [void](Invoke-RepoCompose @('stop', 'mongo'))
            [void](Invoke-RepoCompose @('run', '--rm', '--no-deps', '--user', 'root', '--entrypoint', 'chown', 'mongo', '-R', '0:0', '/bitnami/mongodb'))
            [void](Invoke-RepoCompose @('run', '--rm', '--no-deps', '--user', 'root', '--entrypoint', 'chmod', 'mongo', '-R', 'go-w', '/bitnami/mongodb'))
            [void](Invoke-RepoCompose @('up', '-d', '--no-build', 'mongo'))
        }
        Symptom = 'The dashboard shows no jobs and cannot reach the database. The mongo container keeps restarting.'
        RootCause = @(@('mongo'), @('permission|owner|chown|uid ?1001|not writable|read-only|Permission denied'))
        Healthy = { (Test-ServiceRunning 'mongo') -and ((Invoke-RepoCompose @('exec', '-T', 'mongo', 'sh', '-c', 'test -w /bitnami/mongodb/data/db || test -w /bitnami/mongodb')).Code -eq 0) }
        Restore = {
            [void](Invoke-RepoCompose @('stop', 'mongo'))
            [void](Invoke-RepoCompose @('run', '--rm', '--no-deps', '--user', 'root', '--entrypoint', 'chown', 'mongo', '-R', '1001:1001', '/bitnami/mongodb'))
            [void](Invoke-RepoCompose @('up', '-d', '--no-build', 'mongo'))
        }
    },
    @{
        Id = 'unity-client-gone'
        Source = 'Unity client and its wrapper not running after a sign-in (EVAL timed out waiting for reset)'
        Inject = { Stop-UnityStack }
        Symptom = 'An EVAL job prints "run policy" and then "timed out waiting for reset (>20s). Unity never sent RESTARTED".'
        RootCause = @(@('unity|client|wrapper|simulator'), @('not running|no .*process|gone|stopped|exited|not started|never (started|launched)|closed|isn''t running'))
        Reject = @('(?<!\bnot (a |an |the )?\w*\s?)(binary|build|executable|exe)\b.{0,40}(missing|absent|not found)', '(?<!\bnot (a |an |the )?)(missing|absent).{0,40}\b(binary|build|executable)')
        Healthy = { [bool](Get-UnityClients) }
        Restore = { Start-UnityLauncher }
    },
    @{
        Id = 'unity-stale-after-ros-restart'
        Source = 'Unity client started before the current ros-server never repeats its ROS handshake'
        Inject = { [void](Invoke-RepoCompose @('restart', 'ros-server')) }
        Symptom = 'The Unity window is open, but training jobs never get any observations; ros-server logs show no "ROS-Unity Handshake received" since it started.'
        RootCause = @(@('unity|client'), @('ros-server.{0,60}(restart|recreat|replac)|restarted|recreated|replaced|stopped and started|started before|older than|stale|re-?connect|before .{0,30}ros-server'))
        Reject = @('firewall', 'port mapping')
        Healthy = {
            $id = (Invoke-RepoCompose @('ps', '-q', 'ros-server')).Out.Trim()
            if (-not $id) { return $false }
            $startedAt = (Invoke-Docker @('inspect', '-f', '{{.State.StartedAt}}', $id) $RepoDir).Out.Trim()
            $logs = (Invoke-Docker @('logs', '--since', $startedAt, $id) $RepoDir).Out
            $started = ([datetime]$startedAt).ToLocalTime()
            [bool](Get-UnityClients | Where-Object { $_.StartTime -gt $started }) -or ($logs -match 'ROS-Unity Handshake received')
        }
        Restore = { Stop-UnityStack; Start-UnityLauncher }
    },
    @{
        Id = 'images-missing'
        Source = 'Docker Desktop reinstalled; compose up fails with "pull access denied for sim_controller"'
        Inject = {
            # A neutral tag keeps the layers so Restore is instant.
            [void](Invoke-Docker @('tag', 'sim_controller:latest', 'sim_controller:prev-build') $RepoDir)
            [void](Invoke-RepoCompose @('rm', '-sf', 'sim-controller'))
            [void](Invoke-Docker @('rmi', 'sim_controller:latest') $RepoDir)
        }
        Symptom = 'Starting the stack prints: pull access denied for sim_controller, repository does not exist or may require ''docker login''.'
        RootCause = @(@('image'), @('missing|not (been )?(present|available|found|built)|never built|without building|deleted|removed|does not exist|doesn''t exist|no local|no such image|different tag|non-?existent'))
        Reject = @('dockerfile. key', 'non-standard', 'incorrectly specifies')
        Healthy = { ((Invoke-Docker @('image', 'inspect', 'sim_controller:latest') $RepoDir).Code -eq 0) -and (Test-ServiceRunning 'sim-controller') }
        Restore = {
            foreach ($backup in 'sim_controller:prev-build', 'sim_controller:doctor-eval') {
                if ((Invoke-Docker @('image', 'inspect', 'sim_controller:latest') $RepoDir).Code -ne 0) {
                    [void](Invoke-Docker @('tag', $backup, 'sim_controller:latest') $RepoDir)
                }
                [void](Invoke-Docker @('rmi', $backup) $RepoDir)
            }
            [void](Invoke-RepoCompose @('up', '-d', '--no-build', 'sim-controller'))
        }
    },
    @{
        Id = 'port-reserved'
        Source = 'Windows excluded port range covering a published port (fly-brain 50061)'
        Requires = 'admin'
        Inject = {
            [void](Invoke-RepoCompose @('stop', 'mongo-express'))
            & netsh interface ipv4 add excludedportrange protocol=tcp startport=8081 numberofports=1 | Out-Null
        }
        Symptom = 'docker compose up fails: ports are not available: exposing port TCP 127.0.0.1:8081: bind: An attempt was made to access a socket in a way forbidden by its access permissions.'
        RootCause = @(@('8081'), @('exclu|reserved|winnat|hyper-v|netsh'))
        Healthy = { Test-ServiceRunning 'mongo-express' }
        Restore = {
            & netsh interface ipv4 delete excludedportrange protocol=tcp startport=8081 numberofports=1 | Out-Null
            [void](Invoke-RepoCompose @('up', '-d', '--no-build', 'mongo-express'))
        }
    },
    @{
        Id = 'dashboard-package-json'
        Source = 'npm error ENOENT: open /dashboard/package.json'
        Inject = {
            Hide-File (Join-Path $RepoDir 'dashboard\package.json')
            [void](Invoke-RepoCompose @('restart', 'dashboard'))
        }
        Symptom = 'http://localhost does not load. The dashboard container log shows: npm error code ENOENT ... open ''/dashboard/package.json''.'
        RootCause = @(@('package\.json'), @('missing|not found|moved|renamed|deleted|ENOENT|lacks|absent|no package\.json'))
        Healthy = { (Test-Path (Join-Path $RepoDir 'dashboard\package.json')) -and (Test-ServiceRunning 'dashboard') }
        Restore = {
            Restore-File (Join-Path $RepoDir 'dashboard\package.json')
            [void](Invoke-RepoCompose @('restart', 'dashboard'))
        }
    },
    @{
        Id = 'docker-other-account'
        Source = 'Docker Desktop already running for another Windows account'
        Requires = 'second-account'
        Inject = { throw 'Manual: sign in to a second account, start Docker Desktop there, switch back without signing out.' }
        Symptom = 'The installer starts Docker Desktop, but "docker info" never succeeds and Docker Desktop shows no window.'
        RootCause = @(@('another|other|second|different'), @('account|user|session'))
        Healthy = { (Invoke-Docker @('info') $RepoDir).Code -eq 0 }
        Restore = { }
    }
)
