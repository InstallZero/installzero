//go:build !windows && !darwin

package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const storeName = "the Secret Service keyring"

// secretTool runs libsecret's secret-tool; the key goes on standard input.
func secretTool(stdin string, args ...string) (string, error) {
	path, err := exec.LookPath("secret-tool")
	if err != nil {
		return "", ErrUnavailable
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if msg := strings.TrimSpace(errOut.String()); msg != "" {
			// No D-Bus session or no keyring daemon.
			if strings.Contains(msg, "org.freedesktop") || strings.Contains(msg, "D-Bus") || strings.Contains(msg, "DBUS") {
				return "", ErrUnavailable
			}
			return "", fmt.Errorf("secret-tool: %s", msg)
		}
		return "", ErrNotFound
	}
	return out.String(), err
}

func get(provider string) (string, error) {
	out, err := secretTool("", "lookup", "service", service, "provider", provider)
	if err == nil && out == "" {
		return "", ErrNotFound
	}
	return strings.TrimRight(out, "\n"), err
}

func set(provider, key string) error {
	_, err := secretTool(key, "store", "--label=InstallZero "+provider+" API key", "service", service, "provider", provider)
	return err
}

func del(provider string) error {
	if _, err := get(provider); err != nil {
		return err
	}
	_, err := secretTool("", "clear", "service", service, "provider", provider)
	return err
}
