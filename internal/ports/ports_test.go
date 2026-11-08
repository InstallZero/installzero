package ports

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/InstallZero/installzero/internal/installmd"
	"github.com/InstallZero/installzero/internal/runner"
	"github.com/InstallZero/installzero/internal/target"
)

// fake answers scripts by substring and records what ran.
type fake struct {
	answers map[string]string
	ran     []string
}

func (f *fake) Run(_ context.Context, _, code, _ string, _ time.Duration) target.Result {
	f.ran = append(f.ran, code)
	for k, v := range f.answers {
		if strings.Contains(code, k) {
			return target.Result{Out: v}
		}
	}
	return target.Result{}
}

const jenkinsJournal = `Started jenkins.service - Jenkins Continuous Integration Server.
SEVERE	hudson.lifecycle.Lifecycle#onStatusUpdate: Failed to bind to 0.0.0.0/0.0.0.0:8080
java.io.IOException: Failed to bind to 0.0.0.0/0.0.0.0:8080
Caused by: java.net.BindException: Address already in use
jenkins.service: Main process exited, code=exited, status=1/FAILURE
jenkins.service: Start request repeated too quickly.
jenkins.service: Failed with result 'exit-code'.`

const jenkinsStep = `mkdir -p /etc/systemd/system/jenkins.service.d
printf '[Service]\nEnvironment="JENKINS_PORT=8095"\n' > /etc/systemd/system/jenkins.service.d/override.conf
systemctl daemon-reload
systemctl enable --now jenkins
systemctl restart jenkins`

func TestDeclared(t *testing.T) {
	got, err := Declared("web=8095, agent=50000")
	if err != nil || !reflect.DeepEqual(got, map[string]int{"web": 8095, "agent": 50000}) {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"web", "web=x", "web=70000", "=80"} {
		if _, err := Declared(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
	if Placeholder("ui-web") != "PORT_UI_WEB" {
		t.Error(Placeholder("ui-web"))
	}
}

func TestUsed(t *testing.T) {
	got := Used(jenkinsStep, "curl -fsS http://localhost:8095/login", "docker run -p 6006:6006 tb", "docker run -p 127.0.0.1:5433:5432 pg", "listen 80;", "git clone https://github.com:443/x")
	want := []int{80, 5433, 6006, 8095}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseListeners(t *testing.T) {
	m := map[int]Listener{}
	parseListeners("8095\tjava pid 8477\tjenkins.service\n22\tsshd pid 90\t\nbad line\n", "", m)
	parseListeners("8080\tsvchost pid 4\tW3SVC\r\n22\tother pid 1\t\r\n", "Windows", m)
	if m[8095].Unit != "jenkins.service" || m[22].Process != "sshd pid 90" || m[8080].Where != "Windows" {
		t.Fatalf("%+v", m)
	}
	if s := m[8080].String(); s != "svchost pid 4 (W3SVC) on Windows" {
		t.Error(s)
	}
}

func TestParseContainers(t *testing.T) {
	m := map[int]Listener{}
	parseListeners("22\tsshd pid 1\t\n8080\t\t\n", "", m)
	parseContainers("rl-roboracer-dashboard-1\t0.0.0.0:80->80/tcp, [::]:80->80/tcp, 0.0.0.0:8080->8080/tcp, [::]:8080->8080/tcp\r\nidle\t\r\n", "Docker Desktop", m)
	if s := m[8080].String(); s != "container rl-roboracer-dashboard-1 on Docker Desktop" {
		t.Fatal(s)
	}
	if _, ok := m[80]; !ok || m[22].Process != "sshd pid 1" {
		t.Fatalf("%+v", m)
	}
}

func TestResolve(t *testing.T) {
	busy := map[int]Listener{8095: {Process: "nginx pid 1"}, 8096: {Process: "x pid 2"}}
	got := Resolve(map[string]int{"web": 8095, "agent": 50000}, busy, false)
	if got[0].Name != "agent" || got[0].Port != 50000 || got[1].Port != 8097 || got[1].Owner == nil {
		t.Fatalf("%+v", got)
	}
	got = Resolve(map[string]int{"web": 8095}, busy, true)
	if got[0].Port != 8095 || !got[0].Reused {
		t.Fatalf("a finished install keeps its port: %+v", got)
	}
}

// The journal of the WSL replay: the apt package's first starts of Jenkins
// on 8080 while a Docker Desktop container held it. Log times and stack
// trace line numbers must not pass for ports.
func TestDiagnoseRealJenkinsJournal(t *testing.T) {
	b, err := os.ReadFile("testdata/jenkins-journal.txt")
	if err != nil {
		t.Fatal(err)
	}
	e := &fake{answers: map[string]string{"journalctl -u": string(b)}}
	d := Diagnose(context.Background(), e, false, jenkinsStep, "Job for jenkins.service failed because the control process exited with error code.")
	if d == nil || d.Port != 8080 || !d.StartLimit {
		t.Fatalf("%+v", d)
	}
}

func TestDiagnose(t *testing.T) {
	e := &fake{answers: map[string]string{"journalctl -u": jenkinsJournal}}
	d := Diagnose(context.Background(), e, false, jenkinsStep, "Job for jenkins.service failed because the control process exited with error code.\nSee \"journalctl -xeu jenkins.service\" for details.")
	if d == nil || d.Port != 8080 || !d.StartLimit || !reflect.DeepEqual(d.Units, []string{"jenkins.service"}) {
		t.Fatalf("%+v", d)
	}
	for out, port := range map[string]int{
		"Error response from daemon: driver failed programming external connectivity: Bind for 0.0.0.0:6006 failed: port is already allocated":                                             6006,
		"nginx: [emerg] bind() to 0.0.0.0:80 failed (98: Address already in use)":                                                                                                          80,
		"FATAL:  could not create any TCP/IP sockets\nLOG:  could not bind IPv4 address \"127.0.0.1\": Address already in use\nHINT:  Is another postmaster already running on port 5432?": 5432,
		"Error: listen EADDRINUSE: address already in use :::3000":                                                                                                                         3000,
		"OSError: [Errno 98] Address already in use":                                                                                                                                       0,
	} {
		d := Diagnose(context.Background(), &fake{}, false, "", out)
		if d == nil || d.Port != port {
			t.Errorf("%q: got %+v, want port %d", out, d, port)
		}
	}
	if Diagnose(context.Background(), &fake{}, false, "", "E: Unable to locate package foo") != nil {
		t.Error("not a bind failure")
	}
}

func failure(code string) runner.Failure {
	return runner.Failure{Step: &runner.Step{Step: &installmd.Step{ID: "start"}, Install: &runner.Block{Block: &installmd.Block{Code: code}}}, Output: "Job for jenkins.service failed.", Attempt: 1}
}

func TestFixDefaultPortOfAPackage(t *testing.T) {
	// The package started Jenkins on 8080, which Windows holds; the step
	// moves it to 8095, so the fix is to clear systemd's start limit.
	e := &fake{answers: map[string]string{"journalctl": jenkinsJournal}}
	f := Fixer{Exec: e, PlanPorts: map[int]bool{8095: true},
		Listening: func(context.Context) map[int]Listener {
			return map[int]Listener{8080: {Process: "svchost pid 4", Where: "Windows"}}
		}}
	x := f.Fix(context.Background(), failure(jenkinsStep))
	if x.Action != "retry" || !strings.Contains(x.Note, "reset jenkins.service") {
		t.Fatalf("%+v", x)
	}
	if !strings.Contains(strings.Join(e.ran, "\n"), "systemctl reset-failed 'jenkins.service'") && !strings.Contains(strings.Join(e.ran, "\n"), "systemctl reset-failed jenkins.service") {
		t.Fatalf("ran %q", e.ran)
	}
}

func TestFixPlanPortHeldBySomethingElse(t *testing.T) {
	journal := strings.ReplaceAll(jenkinsJournal, "8080", "8095")
	held := func(l Listener) func(context.Context) map[int]Listener {
		return func(context.Context) map[int]Listener { return map[int]Listener{8095: l} }
	}
	asked := ""
	f := Fixer{Exec: &fake{answers: map[string]string{"journalctl": journal}}, PlanPorts: map[int]bool{8095: true},
		Listening: held(Listener{Process: "nginx pid 7", Unit: "nginx.service"}),
		Confirm:   func(q string) bool { asked = q; return true }}
	if x := f.Fix(context.Background(), failure(jenkinsStep)); x.Action != "retry" || !strings.Contains(asked, "Stop nginx.service?") {
		t.Fatalf("%+v asked %q", x, asked)
	}

	f.Confirm = func(string) bool { return false }
	if x := f.Fix(context.Background(), failure(jenkinsStep)); x.Action != "give-up" || !strings.Contains(x.Note, "declare the port") {
		t.Fatalf("declined: %+v", x)
	}

	f.Confirm = func(string) bool { t.Fatal("cannot stop a Windows program from the target"); return false }
	f.Listening = held(Listener{Process: "svchost pid 4", Unit: "W3SVC", Where: "Windows"})
	if x := f.Fix(context.Background(), failure(jenkinsStep)); x.Action != "give-up" || !strings.Contains(x.Note, "on Windows") {
		t.Fatalf("windows owner: %+v", x)
	}
}

func TestFixHandsOtherFailuresOn(t *testing.T) {
	f := Fixer{Exec: &fake{}, Listening: func(context.Context) map[int]Listener { return nil }}
	x := f.Fix(context.Background(), runner.Failure{Step: &runner.Step{Step: &installmd.Step{ID: "s"}}, Output: "E: Unable to locate package foo", Attempt: 1})
	if x.Action != "give-up" || x.Note != "" {
		t.Fatalf("%+v", x)
	}
}
