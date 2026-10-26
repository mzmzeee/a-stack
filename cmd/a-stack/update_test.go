package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// fakeBinary is a release binary: a script that prints its version, and
// logs refresh calls to $A_STACK_TEST_LOG.
func fakeBinary(tag string) string {
	return "#!/bin/sh\nif [ \"$1\" = update ]; then echo \"$*\" >> \"$A_STACK_TEST_LOG\"; if [ \"$A_STACK_TEST_REFRESH\" = 1 ]; then echo '{\"data\":{\"skills\":[\"/skills/a-stack\"],\"refreshed_files\":[\"/skills/a-stack/SKILL.md\",\"/pi/a-stack.ts\"]}}'; else echo '{\"data\":{\"skills\":[],\"refreshed_files\":[]}}'; fi; exit 0; fi\necho '{\"data\":{\"version\":\"" + tag + "\"}}'\n"
}

func tarGz(t testing.TB, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if tw.Close() != nil || gz.Close() != nil {
		t.Fatal("cannot write archive")
	}
	return buf.Bytes()
}

// testKey signs the fake releases; updateSetup makes update trust it.
var testKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))

// fakeRelease serves releases like GitHub: /latest redirects to the tag of
// latest, and /download/TAG/ has the archive, SHA256SUMS and SHA256SUMS.sig.
// sig is "" for a good signature, "bad" or "missing".
type fakeRelease struct {
	latest   string
	archives map[string][]byte
	badSum   bool
	sig      string
}

func (f *fakeRelease) sums(archive []byte) []byte {
	sum := sha256.Sum256(archive)
	if f.badSum {
		sum[0]++
	}
	return []byte(hex.EncodeToString(sum[:]) + "  " + releaseArchive(runtime.GOOS, runtime.GOARCH) + "\nabc  install.sh\n")
}

func (f *fakeRelease) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/latest" {
		http.Redirect(w, r, "/tag/"+f.latest, http.StatusFound)
		return
	}
	tag, file, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/download/"), "/")
	archive, ok := f.archives[tag]
	name := releaseArchive(runtime.GOOS, runtime.GOARCH)
	switch {
	case !ok:
		http.NotFound(w, r)
	case file == name:
		_, _ = w.Write(archive)
	case file == "SHA256SUMS":
		_, _ = w.Write(f.sums(archive))
	case file == "SHA256SUMS.sig" && f.sig != "missing":
		sig := ed25519.Sign(testKey, f.sums(archive))
		if f.sig == "bad" {
			sig[0]++
		}
		_, _ = w.Write(sig)
	default:
		http.NotFound(w, r)
	}
}

// updateSetup installs an old binary of version current in a temp folder and
// points update at a fake release server.
func updateSetup(t *testing.T, current string, release *fakeRelease) (exe, log string) {
	t.Helper()
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("update supports amd64 and arm64")
	}
	unixOnly(t, "the fake release binaries are shell scripts; update_zip_test.go has the Windows steps")
	server := httptest.NewServer(release)
	t.Cleanup(server.Close)
	t.Setenv("A_STACK_BASE_URL", server.URL)
	hookHome(t)
	t.Chdir(t.TempDir())
	log = filepath.Join(t.TempDir(), "log")
	t.Setenv("A_STACK_TEST_LOG", log)
	t.Setenv("A_STACK_TEST_REFRESH", "")
	exe = filepath.Join(t.TempDir(), "a-stack")
	if err := os.WriteFile(exe, []byte("old"), 0o750); err != nil {
		t.Fatal(err)
	}
	oldVersion, oldExecutable, oldService, oldKey := version, executable, updateService, releaseKey
	version, executable, updateService, releaseKey = current, func() (string, error) { return exe, nil }, func() (string, string, bool) { return "", "", false }, testKey.Public().(ed25519.PublicKey)
	t.Cleanup(func() { version, executable, updateService, releaseKey = oldVersion, oldExecutable, oldService, oldKey })
	return exe, log
}

func runUpdate(t *testing.T, wantCode int, args ...string) (string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	if code := run(append([]string{"update"}, args...), &out, &errout); code != wantCode {
		t.Fatalf("update %v: exit %d, want %d; stdout=%s stderr=%s", args, code, wantCode, out.String(), errout.String())
	}
	return out.String(), errout.String()
}

func checkFile(t *testing.T, path, want string) {
	t.Helper()
	if data, err := os.ReadFile(path); err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v; want %q", path, data, err, want)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files stay next to %s: %v", path, entries)
	}
}

func TestUpdateReplacesTheBinary(t *testing.T) {
	release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.2.0"), "README.md": "x"})}}
	exe, log := updateSetup(t, "v0.1.0", release)
	out, _ := runUpdate(t, 0)
	if out != "Updated a-stack v0.1.0 -> v0.2.0 in "+exe+"\nIf a-stack serve is running, stop it with Ctrl-C and run a-stack serve again.\nThen run a-stack to open the view.\n" {
		t.Fatalf("output %q", out)
	}
	checkFile(t, exe, fakeBinary("v0.2.0"))
	if info, _ := os.Stat(exe); info.Mode().Perm() != 0o750 {
		t.Fatalf("mode %v, want 0750", info.Mode())
	}
	if data, _ := os.ReadFile(log); string(data) != "update --refresh --json\n" {
		t.Fatalf("refresh calls %q", data)
	}

	// Up to date now; with a skill copy, a new version refreshes it.
	version = "v0.2.0"
	if out, _ := runUpdate(t, 0); out != "a-stack v0.2.0 is already the latest version\n" {
		t.Fatalf("output %q", out)
	}
	runSkill(t, "skill", "install", "--project")
	t.Setenv("A_STACK_TEST_REFRESH", "1")
	release.latest, release.archives["v0.3.0"] = "v0.3.0", tarGz(t, map[string]string{"./a-stack": fakeBinary("v0.3.0")})
	updateService = func() (string, string, bool) { return exe, "", true }
	out, _ = runUpdate(t, 0, "--json", "--no-restart")
	var result struct {
		Data struct {
			Previous, Version, Path string
			RestartHint             string `json:"restart_hint"`
			Updated                 bool
			Skills                  []string
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	d := result.Data
	if d.Previous != "v0.2.0" || d.Version != "v0.3.0" || d.Path != exe || !d.Updated || strings.Join(d.Skills, ",") != "/skills/a-stack" || d.RestartHint != "Restart the service: "+stack.ServiceCommand("restart") {
		t.Fatalf("json %s", out)
	}
	if data, _ := os.ReadFile(log); string(data) != "update --refresh --json\nupdate --refresh --json\n" {
		t.Fatalf("refresh calls %q", data)
	}
	checkFile(t, exe, fakeBinary("v0.3.0"))
}

func TestUpdateRefusesBadDownloads(t *testing.T) {
	good := tarGz(t, map[string]string{"a-stack": fakeBinary("v0.2.0")})
	for name, release := range map[string]*fakeRelease{
		"checksum":       {latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": good}, badSum: true},
		"path traversal": {latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.2.0"), "../evil": "x"})}},
		"wrong version":  {latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.9.9")})}},
		"no binary":      {latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"other": "x"})}},
		"missing":        {latest: "v0.2.0", archives: map[string][]byte{}},
	} {
		t.Run(name, func(t *testing.T) {
			exe, _ := updateSetup(t, "v0.1.0", release)
			out, errout := runUpdate(t, 1)
			if out != "" || !strings.Contains(errout, "integration_unavailable") {
				t.Fatalf("stdout=%q stderr=%s", out, errout)
			}
			if name == "path traversal" && !strings.Contains(errout, "unsafe path") || name == "checksum" && !strings.Contains(errout, "checksum mismatch") {
				t.Fatalf("stderr %s", errout)
			}
			checkFile(t, exe, "old")
			if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "..", "evil")); !os.IsNotExist(err) {
				t.Fatal("archive wrote outside")
			}
		})
	}
}

func TestUpdateCheckVersionAndRefusals(t *testing.T) {
	release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.1.5": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.1.5")})}}
	exe, _ := updateSetup(t, "v0.1.9", release)
	if out, _ := runUpdate(t, 0, "--check"); out != "a-stack v0.2.0 is available; you have v0.1.9. Run a-stack update to install it.\n" {
		t.Fatalf("check %q", out)
	}
	if out, _ := runUpdate(t, 0, "--check", "--json"); out != `{"data":{"latest":"v0.2.0","restarted":false,"service_version":"","update_available":true,"version":"v0.1.9"}}`+"\n" {
		t.Fatalf("check json %q", out)
	}
	version = "v0.2.0"
	if out, _ := runUpdate(t, 0, "--check"); out != "a-stack v0.2.0 is the latest version\n" {
		t.Fatalf("check %q", out)
	}
	checkFile(t, exe, "old")

	// --version installs an older release; a dev build needs it.
	version = ""
	if _, errout := runUpdate(t, 2); !strings.Contains(errout, "development build") {
		t.Fatalf("dev build: %s", errout)
	}
	if out, _ := runUpdate(t, 0, "--version", "v0.1.5"); !strings.HasPrefix(out, "Updated a-stack dev -> v0.1.5 in "+exe+"\n") {
		t.Fatalf("output %q", out)
	}
	checkFile(t, exe, fakeBinary("v0.1.5"))
	for _, args := range [][]string{{"--version", "latest"}, {"--version=1.2"}, {"--check", "--version", "v0.1.5"}, {"--state-dir", "/x"}, {"--version"}} {
		if _, errout := runUpdate(t, 2, args...); !strings.Contains(errout, "invalid_input") {
			t.Fatalf("%v: %s", args, errout)
		}
	}

	// A folder that is not writable stops the update before the download.
	if os.Geteuid() == 0 {
		t.Skip("root can write to every folder")
	}
	version = "v0.1.0"
	if err := os.Chmod(filepath.Dir(exe), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(exe), 0o755) })
	_, errout := runUpdate(t, 1, "--version", "v0.1.5")
	if !strings.Contains(errout, `"forbidden"`) || !strings.Contains(errout, "cannot write to "+filepath.Dir(exe)) || !strings.Contains(errout, "A_STACK_INSTALL_DIR") {
		t.Fatalf("not writable: %s", errout)
	}
}

func TestUpdateHTTPSOnlyWithoutMirror(t *testing.T) {
	t.Setenv("A_STACK_BASE_URL", "")
	r := newReleases()
	plain, _ := url.Parse("http://example.com/a")
	if r.base != defaultReleases || r.client.CheckRedirect(&http.Request{URL: plain}, nil) == nil {
		t.Fatal("a redirect to http is allowed without a mirror")
	}
	if _, err := r.get("http://example.com/a", 10, false, nil); err == nil {
		t.Fatal("an http URL is allowed without a mirror")
	}
	for _, v := range [][2]string{{"v0.2.0", "v0.1.9"}, {"v1.0.0", "v1.0.0-rc.1"}, {"v1.0.0-rc.2", "v1.0.0-rc.1"}, {"v0.0.1", "dev"}} {
		if !newer(v[0], v[1]) || newer(v[1], v[0]) {
			t.Fatalf("newer(%s, %s)", v[0], v[1])
		}
	}
}

func TestUpdateChecksTheReleaseSignature(t *testing.T) {
	archives := map[string][]byte{}
	for _, tag := range []string{"v0.1.4", "v0.1.5", "v0.2.0"} {
		archives[tag] = tarGz(t, map[string]string{"a-stack": fakeBinary(tag)})
	}
	for _, c := range []struct {
		name, sig, tag, want string
	}{
		{"bad signature", "bad", "v0.2.0", "signature of SHA256SUMS for v0.2.0 is not valid"},
		{"missing on a signed release", "missing", "v0.1.5", "release v0.1.5 has no SHA256SUMS.sig"},
		{"bad signature on an old release", "bad", "v0.1.4", "signature of SHA256SUMS for v0.1.4 is not valid"},
	} {
		t.Run(c.name, func(t *testing.T) {
			exe, _ := updateSetup(t, "v0.1.0", &fakeRelease{latest: "v0.2.0", archives: archives, sig: c.sig})
			out, errout := runUpdate(t, 1, "--version", c.tag)
			if out != "" || !strings.Contains(errout, c.want) || !strings.Contains(errout, "nothing changed") {
				t.Fatalf("stdout=%q stderr=%s", out, errout)
			}
			checkFile(t, exe, "old")
		})
	}

	// A release from before signing installs with a warning. A signed one
	// has no warning.
	exe, _ := updateSetup(t, "v0.1.0", &fakeRelease{latest: "v0.2.0", archives: archives, sig: "missing"})
	out, _ := runUpdate(t, 0, "--version", "v0.1.4")
	if !strings.Contains(out, "\nWarning: a-stack v0.1.4 has no release signature, as every release before v0.1.5; only its SHA-256 checksum was checked.\n") {
		t.Fatalf("output %q", out)
	}
	checkFile(t, exe, fakeBinary("v0.1.4"))
	updateSetup(t, "v0.1.4", &fakeRelease{latest: "v0.2.0", archives: archives})
	out, _ = runUpdate(t, 0, "--json")
	var result struct {
		Data struct {
			Version  string
			Signed   bool
			Warnings []string
		} `json:"data"`
	}
	if json.Unmarshal([]byte(out), &result) != nil || result.Data.Version != "v0.2.0" || !result.Data.Signed || result.Data.Warnings == nil || len(result.Data.Warnings) != 0 {
		t.Fatalf("json %s", out)
	}
}

func TestUpdateWarnsThatAnOlderVersionMayNotOpenTheDatabase(t *testing.T) {
	release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.1.5": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.1.5")})}}
	exe, _ := updateSetup(t, "v0.2.0", release)
	out, _ := runUpdate(t, 0, "--version", "v0.1.5")
	if !strings.Contains(out, "\nWarning: a-stack v0.1.5 is older than v0.2.0 and may not open the current database: serve then stops with exit code 4. To go back, run a-stack update --version v0.2.0, or restore the state.db.pre-vN copy") {
		t.Fatalf("output %q", out)
	}
	checkFile(t, exe, fakeBinary("v0.1.5"))
}

// install.sh checks the signature with the same key as update.
func TestInstallScriptHasTheReleaseKey(t *testing.T) {
	script, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, block, _ := strings.Cut(string(script), "-----BEGIN PUBLIC KEY-----\n")
	block, _, _ = strings.Cut(block, "\n-----END PUBLIC KEY-----")
	der, err := base64.StdEncoding.DecodeString(block)
	if err != nil {
		t.Fatal("install.sh has no PEM public key", err)
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if k, ok := key.(ed25519.PublicKey); err != nil || !ok || !k.Equal(releaseKey) {
		t.Fatal("install.sh and update use different release keys", err)
	}
}
