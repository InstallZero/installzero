// Package source classifies an installzero source argument.
package source

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Kind is how a source should be fetched.
type Kind int

const (
	// KindDir is a local folder that contains install.json.
	KindDir Kind = iota
	// KindGitHub is an owner/repo resolved to a GitHub URL.
	KindGitHub
	// KindGit is a git URL or SSH path.
	KindGit
)

// Source is one resolved argument to add or check.
type Source struct {
	Kind Kind
	Raw  string
	URL  string
	Path string
}

var githubRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Resolve classifies owner/repo, a git URL, or a local folder.
func Resolve(s string) (Source, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Source{}, fmt.Errorf("missing source")
	}
	if strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "ssh://") || strings.HasPrefix(s, "git@") {
		return Source{Kind: KindGit, Raw: s, URL: s}, nil
	}
	if info, err := os.Stat(s); err == nil && info.IsDir() {
		return Source{Kind: KindDir, Raw: s, Path: s}, nil
	}
	if githubRepo.MatchString(s) && !strings.Contains(s, "..") {
		return Source{Kind: KindGitHub, Raw: s, URL: "https://github.com/" + s}, nil
	}
	if strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, ".") || strings.Contains(s, ":") {
		return Source{}, fmt.Errorf("folder %s does not exist", s)
	}
	return Source{}, fmt.Errorf("source %q is not an owner/repo, a git URL, or a folder", s)
}
