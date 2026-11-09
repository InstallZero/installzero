// Package tag is the project a copy of installzero was published with: a
// fixed-size slot in the binary that installzero tag fills in a copy of the
// executable, so one release build serves every project and a downloaded
// copy knows what to install when it is opened without arguments.
package tag

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	header = "\x00InstallZero-project-tag/1\x00"
	size   = 512
)

// slot holds header then the project, NUL-padded. Only installzero tag
// writes it, in the file; the literal is the one copy of header in the binary.
var slot = [size]byte{
	0, 'I', 'n', 's', 't', 'a', 'l', 'l', 'Z', 'e', 'r', 'o', '-',
	'p', 'r', 'o', 'j', 'e', 'c', 't', '-', 't', 'a', 'g', '/', '1', 0,
}

// Project is the owner/repo[@ref] or URL this copy was tagged with, or "".
func Project() string {
	b := slot[len(header):]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

// Write copies the executable in to out with project in its slot ("" clears it).
func Write(in, out, project string) error {
	if strings.ContainsRune(project, 0) || len(project) > size-len(header)-1 {
		return fmt.Errorf("the project must be under %d bytes", size-len(header))
	}
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	at, err := find(data)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	body := data[at+len(header) : at+size]
	clear(body)
	copy(body, project)
	return os.WriteFile(out, data, 0o755)
}

// Read is the project in an executable's slot.
func Read(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	at, err := find(data)
	if err != nil {
		return "", fmt.Errorf("%s: %w", file, err)
	}
	b := data[at+len(header) : at+size]
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b), nil
}

func find(data []byte) (int, error) {
	h := slot[:len(header)]
	at := bytes.Index(data, h)
	if at < 0 || at+size > len(data) {
		return 0, errors.New("not an installzero executable with a project slot (v0.1.9 or later)")
	}
	if bytes.Contains(data[at+1:], h) {
		return 0, errors.New("more than one project slot")
	}
	return at, nil
}
