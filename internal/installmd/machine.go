package installmd

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/InstallZero/installzero/internal/psrx"
)

// Machine is what requirements and block conditions are checked against.
type Machine struct {
	OS        string   `json:"os"`   // windows, linux, macos
	Arch      string   `json:"arch"` // x64, arm64
	GPU       string   `json:"gpu"`  // nvidia, amd, intel, other, none
	GPUs      string   `json:"gpus"`
	RAMGB     float64  `json:"ram_gb"`
	DiskGB    *float64 `json:"disk_gb"` // free space; nil when unknown
	Driver    string   `json:"driver"`  // NVIDIA driver version; "" when none
	OSVersion string   `json:"os_version"`
}

var (
	leadingNumber  = psrx.MustCompile(`^(\d+(\.\d+)?)`)
	leadingVersion = psrx.MustCompile(`^\s*(\d+(\.\d+){0,3})`)
)

// Met reports whether a machine meets one requirement or block condition.
// A min_* value with no number in it does not constrain (like an unknown
// key).
func Met(key, want string, m Machine) bool {
	var values []string
	for _, v := range strings.Split(want, ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			values = append(values, v)
		}
	}
	first := ""
	if len(values) > 0 {
		first = values[0]
	}
	num, hasNum := 0.0, false
	if g := leadingNumber.Groups(first); g != nil {
		if f, err := strconv.ParseFloat(g[1], 64); err == nil {
			num, hasNum = f, true
		}
	}
	switch key {
	case "os":
		return in(m.OS, values)
	case "arch":
		return in(m.Arch, values)
	case "gpu":
		return in("any", values) || in(m.GPU, values)
	case "min_ram_gb":
		return !hasNum || m.RAMGB >= num
	case "min_disk_gb":
		return !hasNum || m.DiskGB == nil || *m.DiskGB >= num
	case "min_driver":
		need := looseVersion(first)
		if need == nil {
			return true
		}
		have := looseVersion(m.Driver)
		return have != nil && compareVersions(*have, *need) >= 0
	}
	return true
}

// Unmet lists a step's requirements the machine does not meet, as readable
// strings; empty when the step applies.
func (s *Step) Unmet(m Machine) []string {
	var out []string
	for _, r := range s.Requires {
		if Met(r.Key, r.Value, m) {
			continue
		}
		var has string
		switch r.Key {
		case "min_ram_gb":
			has = number(m.RAMGB) + " GB"
		case "min_disk_gb":
			if m.DiskGB != nil {
				has = number(*m.DiskGB)
			}
			has += " GB free"
		case "min_driver":
			has = m.Driver
		case "os":
			has = m.OS
		case "arch":
			has = m.Arch
		case "gpu":
			has = m.GPU
		}
		out = append(out, fmt.Sprintf("%s needs %s, this machine has %s", r.Key, r.Value, has))
	}
	return out
}

// Select picks the block of a kind that runs on a machine: its conditions
// hold, it is PowerShell on Windows (unless it says os=windows), and among
// those the one with the most conditions; nil when none fits.
func (s *Step) Select(kind string, m Machine) *Block {
	windows := strings.EqualFold(m.OS, "windows")
	def := "bash"
	if windows {
		def = "powershell"
	}
	var fits []*Block
	for _, b := range s.Blocks {
		if !strings.EqualFold(b.Kind, kind) {
			continue
		}
		ok := true
		for _, w := range b.When {
			if !Met(w.Key, w.Value, m) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		shell := b.Shell
		if shell == "" {
			shell = def
		}
		os, _ := b.When.Get("os")
		if !windows || in(shell, []string{"powershell", "pwsh"}) || strings.EqualFold(os, "windows") {
			fits = append(fits, b)
		}
	}
	if len(fits) == 0 {
		return nil
	}
	sort.SliceStable(fits, func(i, j int) bool { return len(fits[i].When) > len(fits[j].When) })
	return fits[0]
}

// looseVersion reads the leading number or dotted version of a value
// ("550", "550.54.14") as major, minor, build, revision (-1 when absent);
// nil when there is none.
func looseVersion(s string) *[4]int {
	g := leadingVersion.Groups(s)
	if g == nil {
		return nil
	}
	parts := strings.Split(g[1], ".")
	if len(parts) == 1 {
		parts = append(parts, "0")
	}
	v := [4]int{-1, -1, -1, -1}
	for i, p := range parts {
		n, err := strconv.ParseInt(p, 10, 32)
		if err != nil {
			return nil
		}
		v[i] = int(n)
	}
	return &v
}

func compareVersions(a, b [4]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func number(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
