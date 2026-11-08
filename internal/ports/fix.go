package ports

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/target"
)

var (
	bindError = regexp.MustCompile(`(?i)address already in use|EADDRINUSE|port is already allocated|failed to bind|could not bind|bind\(\) to \S+ failed|Only one usage of each socket address|Ports are not available|already running on port|WSAEADDRINUSE`)
	// Address forms only: log lines also hold times (00:12:17.397) and
	// stack traces (Main.java:355).
	portIn   = regexp.MustCompile(`(?i)(?:\b\d{1,3}(?:\.\d{1,3}){3}|\]|::|\*|\blocalhost):(\d{1,5})\b|\bport\s+(\d{1,5})\b`)
	unitIn   = regexp.MustCompile(`systemctl\s+(?:--\S+\s+)*(?:start|restart|enable|reload-or-restart|try-restart)\s+(?:--now\s+)?([\w@.-]+)`)
	startLim = regexp.MustCompile(`(?i)start request repeated too quickly|start-limit-hit`)
)

// Diagnosis is why a step could not bind a port.
type Diagnosis struct {
	Port       int      // 0 when the message names none
	Units      []string // the systemd units the step starts
	StartLimit bool     // systemd stopped restarting a unit
	Evidence   string   // the matching lines
}

// Units lists the systemd units a block starts or enables.
func Units(code string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range unitIn.FindAllStringSubmatch(code, -1) {
		u := m[1]
		if !strings.Contains(u, ".") {
			u += ".service"
		}
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

func lastPort(line string) int {
	m := portIn.FindAllStringSubmatch(line, -1)
	for k := len(m) - 1; k >= 0; k-- {
		for _, g := range m[k][1:] {
			if n, err := strconv.Atoi(g); err == nil && n > 0 && n <= 65535 {
				return n
			}
		}
	}
	return 0
}

// Diagnose looks for a bind failure in a failed step's output and in the
// logs of the units the step starts; nil when there is none.
func Diagnose(ctx context.Context, e Exec, windows bool, code, out string) *Diagnosis {
	d := &Diagnosis{}
	text := out
	if !windows {
		d.Units = Units(code)
		for _, u := range d.Units {
			r := e.Run(ctx, "bash", fmt.Sprintf("journalctl -u %s -n 120 --no-pager -o cat 2>/dev/null", target.Quote(u)), "", time.Minute)
			text += "\n" + r.Out
		}
	}
	d.StartLimit = startLim.MatchString(text)
	lines := strings.Split(text, "\n")
	var ev []string
	for i, l := range lines {
		if !bindError.MatchString(l) {
			continue
		}
		ev = append(ev, strings.TrimSpace(l))
		// The port is on the matching line or just before it (Java prints
		// "Failed to bind to 0.0.0.0/0.0.0.0:8080" above the BindException);
		// the last failure in the logs wins.
		for j := i; j >= 0 && j >= i-2; j-- {
			if p := lastPort(lines[j]); p != 0 {
				d.Port = p
				break
			}
		}
	}
	if len(ev) == 0 {
		return nil
	}
	if len(ev) > 6 {
		ev = ev[:6]
	}
	d.Evidence = strings.Join(ev, "\n")
	return d
}

// Fixer resolves bind failures; other failures go to Next (or end the
// step when there is none).
type Fixer struct {
	Exec    Exec
	Windows bool
	// Listening reads who listens where now.
	Listening func(ctx context.Context) map[int]Listener
	// PlanPorts are the ports the install itself uses (declared and used
	// in its blocks).
	PlanPorts map[int]bool
	// Confirm asks before stopping something else that holds a port.
	Confirm func(question string) bool
	Next    runner.Fixer
}

func (f Fixer) next(ctx context.Context, x runner.Failure) runner.Fix {
	if f.Next != nil {
		return f.Next.Fix(ctx, x)
	}
	return runner.Fix{Action: "give-up"}
}

func (f Fixer) Fix(ctx context.Context, x runner.Failure) runner.Fix {
	code := ""
	if x.Step.Install != nil {
		code = x.Step.Install.Code
	}
	d := Diagnose(ctx, f.Exec, f.Windows, code, x.Output)
	if d == nil {
		return f.next(ctx, x)
	}
	if x.Attempt > 2 {
		return runner.Fix{Action: "give-up", Note: "the port conflict was not resolved:\n" + d.Evidence}
	}
	if d.Port == 0 {
		return runner.Fix{Action: "give-up", Note: "a program could not bind its port, and the message does not say which:\n" + d.Evidence}
	}
	owner, busy := f.Listening(ctx)[d.Port]
	if !busy && Answers(ctx, f.Exec, f.Windows, d.Port) {
		owner, busy = Listener{Process: "a program outside this machine's process list (another WSL distro or a container?)"}, true
	}
	ours := false
	for _, u := range d.Units {
		ours = ours || owner.Unit == u
	}
	switch {
	case !busy || ours || !f.PlanPorts[d.Port]:
		// The port is free now, held by the step's own service, or is not
		// one this install uses (a package started its service on its
		// default port before the step configured it): clear the units'
		// failed state, which also lifts systemd's start limit, and retry.
		why := fmt.Sprintf("port %d was busy when the service first started and is free now", d.Port)
		switch {
		case ours:
			why = fmt.Sprintf("port %d is held by the step's own service", d.Port)
		case busy:
			why = fmt.Sprintf("port %d is the service's default, held by %s, and not a port this install uses", d.Port, owner)
		}
		if len(d.Units) > 0 && !f.Windows {
			var b strings.Builder
			for _, u := range d.Units {
				fmt.Fprintf(&b, "systemctl reset-failed %s 2>/dev/null || true\n", target.Quote(u))
				if ours {
					fmt.Fprintf(&b, "systemctl stop %s 2>/dev/null || true\n", target.Quote(u))
				}
			}
			f.Exec.Run(ctx, "bash", b.String(), "", time.Minute)
			why += "; reset " + strings.Join(d.Units, ", ")
		}
		return runner.Fix{Action: "retry", Note: why + ", retrying"}
	case owner.Where == "" && owner.Unit != "" && f.Confirm != nil && !f.Windows:
		if f.Confirm(fmt.Sprintf("Port %d, which step %s needs, is held by %s. Stop %s?", d.Port, x.Step.ID, owner, owner.Unit)) {
			r := f.Exec.Run(ctx, "bash", "systemctl stop "+target.Quote(owner.Unit), "", 2*time.Minute)
			if r.Code == 0 {
				return runner.Fix{Action: "retry", Note: fmt.Sprintf("stopped %s, which held port %d", owner.Unit, d.Port)}
			}
			return runner.Fix{Action: "give-up", Note: fmt.Sprintf("could not stop %s: %s", owner.Unit, strings.TrimSpace(r.Out))}
		}
		fallthrough
	default:
		return runner.Fix{Action: "give-up", Note: fmt.Sprintf("port %d is held by %s. Stop it, or declare the port in the install.md's front matter (ports: name=%d, used as {{PORT_NAME}}) so installzero can move the install to a free port.", d.Port, owner, d.Port)}
	}
}
