// Package fetch gets a project's source into a folder: git clone when git is
// on the PATH, otherwise (GitHub only) the archive of the ref, so a project
// whose install.md installs git can be fetched before git exists.
package fetch

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/InstallZero/installzero/internal/source"
)

// Repo is a remote project: owner/repo or a git URL, at an optional ref
// (branch, tag or commit; empty is the default branch).
type Repo struct {
	Raw    string
	URL    string
	GitHub string // owner/repo when the project is on GitHub
	Ref    string
}

// Parse reads owner/repo[@ref] or a git URL[@ref]. It returns ok false for
// anything else, such as a local file or folder.
func Parse(s string) (Repo, bool) {
	raw, ref := s, ""
	if i := strings.LastIndex(s, "@"); i > 0 && i > strings.LastIndex(s, "/") {
		s, ref = s[:i], s[i+1:]
	}
	if _, err := os.Stat(raw); err == nil {
		return Repo{}, false
	}
	if strings.HasPrefix(s, ".") || strings.Contains(s, `\`) || strings.HasSuffix(strings.ToLower(s), ".md") {
		return Repo{}, false
	}
	src, err := source.Resolve(s)
	if err != nil || src.Kind == source.KindDir {
		return Repo{}, false
	}
	r := Repo{Raw: raw, URL: src.URL, Ref: ref}
	switch {
	case src.Kind == source.KindGitHub:
		r.GitHub = s
	case strings.HasPrefix(src.URL, "https://github.com/"):
		r.GitHub = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(src.URL, "https://github.com/"), "/"), ".git")
	}
	return r, true
}

// Name is the repository's name, the last part of its path.
func (r Repo) Name() string {
	return strings.TrimSuffix(path.Base(strings.TrimSuffix(r.URL, "/")), ".git")
}

// String is owner/repo or the URL, with @ref when there is one.
func (r Repo) String() string {
	s := r.URL
	if r.GitHub != "" {
		s = r.GitHub
	}
	if r.Ref != "" {
		s += "@" + r.Ref
	}
	return s
}

// Get puts the project into dest, which must not exist yet, and says how.
// progress, when not nil, wraps the archive download's body for a meter.
func (r Repo) Get(ctx context.Context, dest string, progress func(total int64) io.Writer) (string, error) {
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if git, err := exec.LookPath("git"); err == nil {
		args := []string{"clone", "--quiet"}
		if r.Ref != "" {
			args = append(args, "--branch", r.Ref)
		}
		cmd := exec.CommandContext(ctx, git, append(args, r.URL+gitSuffix(r), dest)...)
		out, err := cmd.CombinedOutput()
		if err == nil {
			return "git clone", nil
		}
		os.RemoveAll(dest)
		if r.Ref == "" || r.GitHub == "" {
			return "", fmt.Errorf("git clone %s: %v\n%s", r.URL, err, strings.TrimSpace(string(out)))
		}
		// git clone --branch takes branches and tags, not commits; the archive takes all three.
	}
	if r.GitHub == "" {
		return "", fmt.Errorf("git is not installed, and %s is not on GitHub, whose archives are fetched without git", r.URL)
	}
	if err := r.archive(ctx, dest, progress); err != nil {
		os.RemoveAll(dest)
		return "", err
	}
	return "GitHub's archive; not a git checkout", nil
}

func gitSuffix(r Repo) string {
	if r.GitHub != "" && !strings.HasSuffix(r.URL, ".git") {
		return ".git"
	}
	return ""
}

func (r Repo) archive(ctx context.Context, dest string, progress func(int64) io.Writer) error {
	ref := r.Ref
	if ref == "" {
		ref = "HEAD"
	}
	url := "https://github.com/" + r.GitHub + "/archive/" + ref + ".zip"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s has no branch, tag or commit %s, or is private (%s: %s)", r.GitHub, ref, url, resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".installzero-fetch-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	var w io.Writer = tmp
	if progress != nil {
		w = io.MultiWriter(tmp, progress(resp.ContentLength))
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return unzip(tmp.Name(), dest)
}

// unzip extracts an archive whose entries share one top folder (GitHub's
// <repo>-<ref>/) into dest, without that folder.
func unzip(file, dest string) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		name := f.Name
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		} else {
			continue
		}
		if name == "" {
			continue
		}
		to := filepath.Join(root, filepath.FromSlash(name))
		if to != root && !strings.HasPrefix(to, root+string(filepath.Separator)) {
			return fmt.Errorf("archive entry %s is outside the folder", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(to, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := extract(f, to); err != nil {
			return err
		}
	}
	return nil
}

func extract(f *zip.File, to string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := os.FileMode(0o644)
	if f.Mode()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
