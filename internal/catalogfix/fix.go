// Package catalogfix fixes failed install steps from the failure catalog:
// a failing block names its catalog phase (phase=build), its output is
// matched against the entries for that phase, and the entry's fix action is
// applied (retry, restart Docker Desktop, wsl --update, clear the build
// cache) or, for actions that need the user, its diagnosis and advice end
// the step.
package catalogfix

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/InstallZero/installzero/catalog"
	"github.com/InstallZero/installzero/internal/runner"
)

// Fixer applies catalog fixes; failures with no phase or no matching entry
// go to Next (or end the step when there is none).
type Fixer struct {
	Catalog *catalog.Catalog
	// Exec runs the fix scripts on the target.
	Exec runner.Exec
	// Windows is true when the target is the Windows host, where Docker
	// Desktop and WSL fixes apply.
	Windows bool
	// Confirm asks before a fix that restarts or clears something; nil
	// declines.
	Confirm func(question string) bool
	// Sleep waits before a retry (time.Sleep when nil).
	Sleep func(time.Duration)
	Next  runner.Fixer
}

// RetryWait is the pause before retrying a failure the catalog says is
// transient.
var RetryWait = 10 * time.Second

const refreshPath = `$env:Path = [Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User')
`

const restartDocker = refreshPath + `$exe = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
docker desktop version *> $null
if ($LASTEXITCODE -eq 0) { docker desktop restart *> $null }
else {
    Get-Process -Name 'Docker Desktop' -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Seconds 10
    Start-Process -FilePath $exe | Out-Null
}
$deadline = (Get-Date).AddMinutes(5)
while ((Get-Date) -lt $deadline) {
    docker info *> $null
    if ($LASTEXITCODE -eq 0) { exit 0 }
    Start-Sleep -Seconds 10
}
Write-Output 'Docker Desktop did not come back within 5 minutes after the restart'
exit 1
`

const wslUpdate = `$p = Start-Process wsl.exe -ArgumentList '--update' -Verb RunAs -Wait -PassThru
if ($p.ExitCode -ne 0) { Write-Output "wsl --update exited $($p.ExitCode)" }
`

const pruneBuildCache = refreshPath + `docker builder prune -af
exit $LASTEXITCODE
`

func (f Fixer) next(ctx context.Context, x runner.Failure) runner.Fix {
	if f.Next != nil {
		return f.Next.Fix(ctx, x)
	}
	return runner.Fix{Action: "give-up"}
}

func (f Fixer) Fix(ctx context.Context, x runner.Failure) runner.Fix {
	if f.Catalog == nil || x.Phase == "" {
		return f.next(ctx, x)
	}
	e := f.Catalog.Match(x.Output, x.Phase)
	if e == nil {
		return f.next(ctx, x)
	}
	diag := fmt.Sprintf("[%s] %s", e.ID, e.Diagnosis)
	if seen := e.Line(x.Output); seen != "" {
		if len(seen) > 220 {
			seen = seen[:220] + "..."
		}
		diag += " (seen: " + seen + ")"
	}
	advise := runner.Fix{Action: "give-up", Note: diag + " " + e.Advice}
	confirm := func(q string) bool { return f.Confirm != nil && f.Confirm(q) }
	run := func(script string, timeout time.Duration) (bool, string) {
		r := f.Exec.Run(ctx, "powershell", script, "", timeout)
		return r.Code == 0, strings.TrimSpace(r.Out)
	}

	switch e.Action {
	case "retry":
		sleep := f.Sleep
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(RetryWait)
		return runner.Fix{Action: "retry", Note: diag + "; retrying"}
	case "restart-docker":
		if !f.Windows || !confirm(diag+"\nRestart Docker Desktop now? Running containers stop and restart with it.") {
			return advise
		}
		if ok, out := run(restartDocker, 7*time.Minute); !ok {
			return runner.Fix{Action: "give-up", Note: diag + "; restarting Docker Desktop failed: " + out}
		}
		return runner.Fix{Action: "retry", Note: diag + "; restarted Docker Desktop, retrying"}
	case "wsl-update":
		if !f.Windows || !confirm(diag+"\nRun wsl --update now (needs administrator rights) and restart Docker Desktop?") {
			return advise
		}
		run(wslUpdate, 15*time.Minute)
		if ok, out := run(restartDocker, 7*time.Minute); !ok {
			return runner.Fix{Action: "give-up", Note: diag + "; ran wsl --update, but Docker Desktop did not restart: " + out}
		}
		return runner.Fix{Action: "retry", Note: diag + "; ran wsl --update and restarted Docker Desktop, retrying"}
	case "rebuild-clean":
		if !f.Windows || !confirm(diag+"\nClear Docker's build cache (for all projects on this PC) and build again?") {
			return advise
		}
		if ok, out := run(pruneBuildCache, 15*time.Minute); !ok {
			return runner.Fix{Action: "give-up", Note: diag + "; clearing the build cache failed: " + out}
		}
		return runner.Fix{Action: "retry", Note: diag + "; cleared Docker's build cache, retrying"}
	}
	// none, signout, port-owner, recreate, mongo-owner: what the user (or a
	// project-specific step) must do.
	return advise
}
