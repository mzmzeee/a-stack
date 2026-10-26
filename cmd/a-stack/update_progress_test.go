package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// readSlowly downloads 1 MB in 1 kB reads, 1 ms apart, on a fake clock.
func readSlowly(t *testing.T, cols int, total int64) string {
	t.Helper()
	var out bytes.Buffer
	clock := time.Unix(0, 0)
	p := &progress{w: &out, bar: true, cols: cols, now: func() time.Time { return clock }}
	r := p.download(bytes.NewReader(make([]byte, 1e6)), "a.tar.gz", total)
	buf := make([]byte, 1000)
	for {
		if _, err := r.Read(buf); err == io.EOF {
			break
		}
		clock = clock.Add(time.Millisecond)
	}
	p.end()
	p.step("Checking the checksum")
	return out.String()
}

func TestUpdateProgressBar(t *testing.T) {
	out := readSlowly(t, 80, 1e6)
	frames := strings.Split(out, "\r")[1:]
	// One frame at the start, one every 100 ms of the second, and the last.
	if len(frames) < 10 || len(frames) > 12 {
		t.Fatalf("%d frames, want about 11: %q", len(frames), out)
	}
	frame := regexp.MustCompile(`^Downloading \[[#-]{20}\] +\d+%  \d+\.\d / 1\.0 MB(  \d+ kB/s|  \d+\.\d MB/s)? *$`)
	for _, f := range frames[:len(frames)-1] {
		if !frame.MatchString(f) || len(f) > 79 {
			t.Fatalf("frame %q", f)
		}
	}
	if !strings.Contains(out, "\rDownloading [##########----------]  50%  0.5 / 1.0 MB  1.0 MB/s") {
		t.Fatalf("no frame at half: %q", out)
	}
	if last := frames[len(frames)-1]; last != "Downloading [####################] 100%  1.0 / 1.0 MB  1.0 MB/s\nChecking the checksum\n" {
		t.Fatalf("last frame %q", last)
	}
	if strings.Contains(out, "\x1b") {
		t.Fatal("ANSI escape")
	}

	// A narrow terminal gets no bar and no wrapped line.
	for _, f := range strings.Split(readSlowly(t, 40, 1e6), "\r")[1:] {
		f, _, _ = strings.Cut(f, "\n")
		if len(f) > 39 || strings.Contains(f, "[") {
			t.Fatalf("narrow frame %q", f)
		}
	}
}

func TestUpdateProgressWithUnknownSize(t *testing.T) {
	out := readSlowly(t, 80, -1)
	if strings.Contains(out, "%") || !strings.HasPrefix(out, "\rDownloading  0.0 MB") || !strings.HasSuffix(out, "\rDownloading  1.0 MB  1.0 MB/s\nChecking the checksum\n") {
		t.Fatalf("output %q", out)
	}
}

// terminalUpdate makes update see a terminal on stderr.
func terminalUpdate(t *testing.T) {
	t.Helper()
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	old := stderrTerminal
	stderrTerminal = func(io.Writer) bool { return true }
	t.Cleanup(func() { stderrTerminal = old })
}

func TestUpdateProgressOnATerminal(t *testing.T) {
	release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.2.0")})}}
	exe, _ := updateSetup(t, "v0.1.0", release)
	terminalUpdate(t)
	out, errout := runUpdate(t, 0)
	if out != "Updated a-stack v0.1.0 -> v0.2.0 in "+exe+"\nIf a-stack serve is running, stop it with Ctrl-C and run a-stack serve again.\nThen run a-stack to open the view.\n" {
		t.Fatalf("stdout %q", out)
	}
	name := releaseArchive(runtime.GOOS, runtime.GOARCH)
	steps := regexp.MustCompile(`^Checking the signature of SHA256SUMS\n(\rDownloading \[[#-]+\] +\d+%  [^\r\n]*)+\nChecking the checksum of ` + regexp.QuoteMeta(name) + `\nChecking that a-stack v0.2.0 runs\nReplacing ` + regexp.QuoteMeta(exe) + `\n$`)
	if !steps.MatchString(errout) || !strings.Contains(errout, "100%") {
		t.Fatalf("stderr %q", errout)
	}

	// NO_COLOR and TERM=dumb print the steps without the bar.
	for _, env := range [][2]string{{"NO_COLOR", "1"}, {"TERM", "dumb"}} {
		t.Run(env[0], func(t *testing.T) {
			version = "v0.1.0"
			t.Setenv(env[0], env[1])
			out2, errout := runUpdate(t, 0)
			if out2 != out || errout != "Checking the signature of SHA256SUMS\nDownloading "+name+"\nChecking the checksum of "+name+"\nChecking that a-stack v0.2.0 runs\nReplacing "+exe+"\n" {
				t.Fatalf("stdout %q stderr %q", out2, errout)
			}
		})
	}

	// --json prints nothing on stderr.
	version = "v0.1.0"
	if _, errout := runUpdate(t, 0, "--json"); errout != "" {
		t.Fatalf("stderr %q", errout)
	}
}

func TestUpdateProgressNotOnAFile(t *testing.T) {
	release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.2.0")})}}
	exe, _ := updateSetup(t, "v0.1.0", release)
	stderr, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	var out bytes.Buffer
	if code := run([]string{"update"}, &out, stderr); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != "Updated a-stack v0.1.0 -> v0.2.0 in "+exe+"\nIf a-stack serve is running, stop it with Ctrl-C and run a-stack serve again.\nThen run a-stack to open the view.\n" {
		t.Fatalf("stdout %q", out.String())
	}
	if data, _ := os.ReadFile(stderr.Name()); len(data) != 0 {
		t.Fatalf("stderr %q", data)
	}
}

// A download that breaks off ends the bar's line before the error.
func TestUpdateProgressInterruptedDownload(t *testing.T) {
	release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.2.0")})}}
	exe, _ := updateSetup(t, "v0.1.0", release)
	name := releaseArchive(runtime.GOOS, runtime.GOARCH)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if path := r.URL.Path; strings.HasSuffix(path, "/"+name) {
			archive := release.archives["v0.2.0"]
			w.Header().Set("Content-Length", strconv.Itoa(4*len(archive)))
			_, _ = w.Write(archive)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // the connection breaks off
		}
		release.ServeHTTP(w, r)
	}))
	defer server.Close()
	t.Setenv("A_STACK_BASE_URL", server.URL)
	terminalUpdate(t)
	out, errout := runUpdate(t, 1)
	if out != "" || !regexp.MustCompile(`\rDownloading \[[#-]+\]  25%  [^\r\n]*\n\{"error":\{"code":"integration_unavailable","message":"download failed: `).MatchString(errout) {
		t.Fatalf("stdout %q stderr %q", out, errout)
	}
	checkFile(t, exe, "old")
}
