// Package doctor embeds the PowerShell files the Go engine runs as they are.
package doctor

import _ "embed"

// ReadOnlyPS is ReadOnly.ps1: the read-only policy for PowerShell commands.
//
//go:embed ReadOnly.ps1
var ReadOnlyPS string
