package keystore

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const storeName = "the macOS Keychain"

func security(stdin string, args ...string) (string, error) {
	cmd := exec.Command("/usr/bin/security", args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 44: // errSecItemNotFound
		return "", ErrNotFound
	case errors.As(err, &exit):
		return "", fmt.Errorf("security: %s", strings.TrimSpace(errOut.String()))
	case err != nil:
		return "", ErrUnavailable
	}
	return out.String(), nil
}

func get(provider string) (string, error) {
	out, err := security("", "find-generic-password", "-s", service, "-a", provider, "-w")
	return strings.TrimRight(out, "\n"), err
}

// set sends the key on security's standard input (its interactive mode),
// so it never appears in a process list.
func set(provider, key string) error {
	if strings.ContainsAny(key, "\"\\\n") {
		return errors.New("the key has characters the Keychain command cannot take")
	}
	_, err := security(fmt.Sprintf("add-generic-password -U -s %s -a %s -l \"InstallZero %s API key\" -w \"%s\"\n", service, provider, provider, key), "-i")
	return err
}

func del(provider string) error {
	_, err := security("", "delete-generic-password", "-s", service, "-a", provider)
	return err
}
