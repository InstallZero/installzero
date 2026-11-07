// Package agent embeds the PowerShell files the Go engine runs as they are.
package agent

import _ "embed"

// PolicyPS is Policy.ps1: the safety tiers for commands the installer agent
// runs. It needs Test-ReadOnlyCommand from doctor/ReadOnly.ps1.
//
//go:embed Policy.ps1
var PolicyPS string
