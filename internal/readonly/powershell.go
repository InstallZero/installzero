package readonly

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/InstallZero/installzero/doctor"
)

// PowerShell returns, for each command, why it is not read-only ("" when
// it is). It runs doctor/ReadOnly.ps1 itself, in one Windows PowerShell
// process for all the commands: the check walks PowerShell's own syntax
// tree, so PowerShell decides. Elsewhere it uses pwsh, whose parse error
// messages differ.
func PowerShell(ctx context.Context, commands ...string) ([]string, error) {
	if len(commands) == 0 {
		return nil, nil
	}
	var script strings.Builder
	script.WriteString(doctor.ReadOnlyPS)
	script.WriteString("\nforeach ($b in @(")
	for i, c := range commands {
		if i > 0 {
			script.WriteString(",")
		}
		fmt.Fprintf(&script, "'%s'", base64.StdEncoding.EncodeToString([]byte(c)))
	}
	script.WriteString(`)) {
    $r = Test-ReadOnlyCommand ([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($b)))
    if ($null -eq $r) { '-' } else { [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]$r)) }
}
`)
	exe := "pwsh"
	if runtime.GOOS == "windows" {
		exe = "powershell.exe"
	}
	cmd := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command",
		"$s = [Console]::In.ReadToEnd(); . ([scriptblock]::Create($s))")
	cmd.Stdin = strings.NewReader(script.String())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v: %s", exe, err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Fields(string(out))
	if len(lines) != len(commands) {
		return nil, fmt.Errorf("%s returned %d answers for %d commands: %s", exe, len(lines), len(commands), strings.TrimSpace(stderr.String()))
	}
	reasons := make([]string, len(lines))
	for i, l := range lines {
		if l == "-" {
			continue
		}
		b, err := base64.StdEncoding.DecodeString(l)
		if err != nil {
			return nil, fmt.Errorf("%s: answer %d: %v", exe, i, err)
		}
		reasons[i] = string(b)
	}
	return reasons, nil
}
