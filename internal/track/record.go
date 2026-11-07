package track

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Record is what installzero knows about one install: where it went and
// what each step changed. It lives in the state folder
// (<state>/installs/<id>.json), not in the install folder, so it survives
// the uninstall it drives.
type Record struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Target  string        `json:"target"` // local, or wsl:<distro>
	OS      string        `json:"os"`     // windows, linux, macos
	Dir     string        `json:"dir"`
	DirNew  bool          `json:"dir_new"`        // the install folder did not exist before
	Doc     string        `json:"doc"`            // the install.md it came from
	Lock    string        `json:"lock,omitempty"` // installzero install's progress file, on this machine
	Created time.Time     `json:"created"`
	Updated time.Time     `json:"updated"`
	Runs    int           `json:"runs"`
	Steps   []*StepRecord `json:"steps"`
	Backups []string      `json:"backups,omitempty"` // archives of /etc on the target, one per run with a machine-wide step
}

// StepRecord is one step that installzero ran (not one it found done).
type StepRecord struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	Changes   []Change  `json:"changes"`
	Uninstall *Block    `json:"uninstall,omitempty"` // the install.md's uninstall block for this machine
	At        time.Time `json:"at"`
}

// Block is an install.md block kept in a record.
type Block struct {
	Shell string `json:"shell"`
	Code  string `json:"code"`
}

// ID names an install: the same project, target and folder is the same
// install, so a rerun adds to its record.
func ID(name, tgt, dir string) string {
	h := sha256.Sum256([]byte(strings.ToLower(tgt + "\x00" + dir)))
	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			return r
		}
		return '-'
	}, strings.ToLower(name))
	return safe + "-" + hex.EncodeToString(h[:])[:8]
}

// Store is the folder of records.
type Store struct{ Dir string }

func (s Store) path(id string) string { return filepath.Join(s.Dir, "installs", id+".json") }

// Load reads a record; nil when there is none.
func (s Store) Load(id string) (*Record, error) {
	b, err := os.ReadFile(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Record
	return &r, json.Unmarshal(b, &r)
}

// Save writes a record atomically.
func (s Store) Save(r *Record) error {
	r.Updated = time.Now()
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	p := s.path(r.ID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// List reads every record, newest first.
func (s Store) List() ([]*Record, error) {
	ents, err := os.ReadDir(filepath.Join(s.Dir, "installs"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Record
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() {
			continue
		}
		r, err := s.Load(id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Retire moves an uninstalled record to installs/removed, keeping it for
// reference.
func (s Store) Retire(r *Record) error {
	dst := filepath.Join(s.Dir, "installs", "removed", r.ID+"-"+time.Now().Format("20060102-150405")+".json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(s.path(r.ID), dst)
}

// Step returns the record of a step, adding it when missing.
func (r *Record) Step(id, title string) *StepRecord {
	for _, s := range r.Steps {
		if s.ID == id {
			return s
		}
	}
	s := &StepRecord{ID: id, Title: title}
	r.Steps = append(r.Steps, s)
	return s
}

// Add merges changes into a step, skipping ones it already has.
func (s *StepRecord) Add(changes []Change) {
	have := map[string]bool{}
	for _, c := range s.Changes {
		have[c.Kind+"\x00"+c.Key] = true
	}
	for _, c := range changes {
		if !have[c.Kind+"\x00"+c.Key] {
			s.Changes = append(s.Changes, c)
			have[c.Kind+"\x00"+c.Key] = true
		}
	}
}

// Changes is every change of the install, in step order.
func (r *Record) Changes() []Change {
	var out []Change
	for _, s := range r.Steps {
		out = append(out, s.Changes...)
	}
	return out
}
