// Package state is the per-user InstallZero directory: lock file and records.
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// Dir is %LOCALAPPDATA%\installzero on Windows and
// ~/.local/state/installzero elsewhere. INSTALLZERO_STATE overrides it.
func Dir() (string, error) {
	if d := os.Getenv("INSTALLZERO_STATE"); d != "" {
		return d, nil
	}
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "installzero"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "installzero"), nil
}

// Project is one installed project recorded in lock.json.
type Project struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// Projects reads lock.json. A missing file is an empty list.
func Projects(dir string) ([]Project, error) {
	body, err := os.ReadFile(filepath.Join(dir, "lock.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lock struct {
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal(body, &lock); err != nil {
		return nil, err
	}
	return lock.Projects, nil
}

// Record is the header of one local failure record.
type Record struct {
	Dir   string
	ID    string `json:"id"`
	Phase struct {
		ID string `json:"id"`
	} `json:"phase"`
	Signature struct {
		Lines []string `json:"lines"`
	} `json:"signature"`
	Match struct {
		Entry string `json:"entry"`
	} `json:"match"`
}

// Records reads records/*/record.json, newest folder name first.
func Records(dir string) ([]Record, error) {
	root := filepath.Join(dir, "records")
	ents, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, ent.Name(), "record.json"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var rec Record
		if err := json.Unmarshal(body, &rec); err != nil {
			return nil, err
		}
		rec.Dir = ent.Name()
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Dir > out[j].Dir })
	return out, nil
}
