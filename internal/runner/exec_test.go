package runner

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestInvokeIzDownload(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell only")
	}
	body := bytes.Repeat([]byte("x"), 3<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/gym.zip" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Write(body)
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "gym.zip")
	r := Local{}.Run(context.Background(), "powershell", "Invoke-IzDownload -Uri '"+srv.URL+"/gym.zip' -OutFile '"+out+"'", "", time.Minute)
	if st, err := os.Stat(out); r.Code != 0 || err != nil || st.Size() != int64(len(body)) {
		t.Fatalf("exit %d, %v\n%s", r.Code, err, r.Out)
	}
	if !strings.Contains(r.Out, "##progress 100 gym.zip") || !strings.Contains(r.Out, "Downloading "+srv.URL) {
		t.Fatalf("no progress:\n%s", r.Out)
	}
	r = Local{}.Run(context.Background(), "powershell", "Invoke-IzDownload -Uri '"+srv.URL+"/missing' -OutFile '"+out+"'\n'after'", "", time.Minute)
	if r.Code != 1 || strings.Contains(r.Out, "after") || !strings.Contains(r.Out, "404") {
		t.Fatalf("a failed download: exit %d\n%s", r.Code, r.Out)
	}
}

func TestPowerShellErrors(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows PowerShell only")
	}
	cases := []struct {
		name, code string
		want       int
		out        string
	}{
		{"stderr captured", "$o = cmd /c 'echo pulling 1>&2' 2>&1 | Out-String\n'after: ' + $o.Trim()", 0, "after: "},
		{"stderr discarded", "cmd /c 'echo pulling 1>&2' 2>$null\n'after'", 0, "after"},
		{"Write-Error", "Write-Error 'boom'\n'after'", 1, "boom"},
		{"failing cmdlet", "Get-Item C:\\no\\such\\path\n'after'", 1, "Cannot find path"},
		{"cmdlet told to continue", "Get-Item C:\\no\\such\\path -ErrorAction SilentlyContinue\n'after'", 0, "after"},
		{"uncaught exception", "[IO.File]::ReadAllText('C:\\no\\such\\file')\n'after'", 1, "Could not find"},
		{"caught exception", "try { [IO.File]::ReadAllText('C:\\no\\such\\file') } catch { 'caught' }\n'after'", 0, "after"},
		{"throw in a function", "function F { throw 'bad' }\nF\n'after'", 1, "bad"},
		{"exit code", "exit 3010", 3010, ""},
		{"program exit code", "cmd /c exit 7", 7, ""},
	}
	for _, c := range cases {
		r := Local{}.Run(context.Background(), "powershell", c.code, "", time.Minute)
		if r.Code != c.want || !strings.Contains(r.Out, c.out) || (c.want != 0 && strings.Contains(r.Out, "after")) {
			t.Errorf("%s: exit %d, want %d\n%s", c.name, r.Code, c.want, r.Out)
		}
	}
}
