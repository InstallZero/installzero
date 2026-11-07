package track

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/InstallZero/installzero/internal/target"
)

// Exec runs a script on the target (runner.Exec).
type Exec interface {
	Run(ctx context.Context, shell, code, dir string, timeout time.Duration) target.Result
}

// Recorder snapshots the target around each step the runner runs and
// saves what changed to the install's record after every step, so a run
// that stops halfway can still be uninstalled.
type Recorder struct {
	Exec   Exec
	Store  Store
	Record *Record
	ctx    Context
	before Snapshot
	backup string // this run's /etc archive, once taken
	Warn   func(string)
}

// NewRecorder loads or starts the record of an install and counts a run.
func NewRecorder(e Exec, st Store, name, tgt, osName, dir, doc string) (*Recorder, error) {
	id := ID(name, tgt, dir)
	r, err := st.Load(id)
	if err != nil {
		return nil, err
	}
	if r == nil {
		r = &Record{ID: id, Name: name, Target: tgt, OS: osName, Dir: dir, Created: time.Now()}
	}
	r.Doc = doc
	r.Runs++
	rec := &Recorder{Exec: e, Store: st, Record: r}
	if osName == "windows" {
		rec.ctx = Context{
			Dir:            dir,
			WindowsData:    []string{os.Getenv("ProgramData"), os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")},
			WindowsIgnored: []string{os.Getenv("TEMP"), st.Dir, filepath.Join(os.Getenv("LOCALAPPDATA"), "Temp")},
		}
	} else {
		rec.ctx = Context{Dir: dir}
	}
	return rec, nil
}

func (r *Recorder) windows() bool { return r.Record.OS == "windows" }

func (r *Recorder) warn(format string, args ...any) {
	if r.Warn != nil {
		r.Warn(fmt.Sprintf(format, args...))
	}
}

// Snapshot takes a snapshot of the target.
func (r *Recorder) Snapshot(ctx context.Context) (Snapshot, error) {
	shell, code := snapshotScript(r.windows(), r.Record.Dir)
	res := r.Exec.Run(ctx, shell, code, "", 10*time.Minute)
	if res.Code != 0 {
		return nil, fmt.Errorf("snapshot exited %d: %s", res.Code, res.Out)
	}
	return parse(res.Out), nil
}

// Before is called before a step's install block runs. machineWide says
// whether the step has machine-wide blocks; before the first such step of
// a run, /etc is archived on the target so files the install changes can
// be restored.
func (r *Recorder) Before(ctx context.Context, stepID string, machineWide bool) {
	if machineWide && r.backup == "" && !r.windows() {
		p := fmt.Sprintf("/var/lib/installzero/backups/%s-%s.tgz", r.Record.ID, time.Now().Format("20060102-150405"))
		res := r.Exec.Run(ctx, "bash", fmt.Sprintf("mkdir -p /var/lib/installzero/backups && tar czf %s -C / etc 2>/dev/null; test -s %s", target.Quote(p), target.Quote(p)), "", 5*time.Minute)
		if res.Code == 0 {
			r.backup = p
			r.Record.Backups = append(r.Record.Backups, p)
		} else {
			r.warn("could not back up /etc (%s); changed files there cannot be restored on uninstall", strings.TrimSpace(res.Out))
		}
	}
	if r.Record.Runs == 1 && len(r.Record.Steps) == 0 {
		r.Record.DirNew = !r.dirExists(ctx)
	}
	s, err := r.Snapshot(ctx)
	if err != nil {
		r.warn("could not record the machine before step %s: %v", stepID, err)
		s = nil
	}
	r.before = s
}

func (r *Recorder) dirExists(ctx context.Context) bool {
	if r.windows() {
		_, err := os.Stat(r.Record.Dir)
		return err == nil
	}
	return r.Exec.Run(ctx, "bash", "test -e "+target.Quote(r.Record.Dir), "", time.Minute).Code == 0
}

// After is called when a step that Before saw has ended, done or not:
// a failed step's changes are recorded too.
func (r *Recorder) After(ctx context.Context, stepID, title, status, code string, uninstall *Block) {
	if r.before == nil {
		return
	}
	after, err := r.Snapshot(ctx)
	if err != nil {
		r.warn("could not record the machine after step %s: %v; uninstall will miss its changes", stepID, err)
		r.before = nil
		return
	}
	c := r.ctx
	c.Code = code
	changes := Diff(r.before, after, c)
	for i := range changes {
		if changes[i].Kind == "modified" {
			changes[i].Backup = r.backup
		}
	}
	s := r.Record.Step(stepID, title)
	s.Status, s.At = status, time.Now()
	s.Add(changes)
	if uninstall != nil {
		s.Uninstall = uninstall
	}
	r.before = nil
	if err := r.Store.Save(r.Record); err != nil {
		r.warn("could not save the install record: %v", err)
	}
}
