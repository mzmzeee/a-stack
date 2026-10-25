package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the installer itself: a valid checksum must not let a broken
// executable replace an existing installation. No PATH or service changes.
func TestPowerShellInstallerRejectsBrokenDownload(t *testing.T) {
	archive := zipArchive(t, "a-stack.exe", "this is not a Windows executable")
	sum := sha256.Sum256(archive)
	script, err := filepath.Abs(filepath.Join("..", "..", "install.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, shell := range []string{"powershell", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			command, err := exec.LookPath(shell)
			if err != nil {
				t.Skip("shell unavailable:", err)
			}
			for _, fixture := range []struct {
				name, marker, hash, suffix, want string
				duplicate                        bool
			}{
				{name: "text", marker: " ", hash: fmt.Sprintf("%x", sum), want: "downloaded binary does not run"},
				{name: "binary", marker: "*", hash: fmt.Sprintf("%x", sum), want: "downloaded binary does not run"},
				{name: "wrong hash", marker: "*", hash: strings.Repeat("0", 64), want: "checksum mismatch"},
				{name: "malformed hash", marker: "*", hash: "1234", want: "checksum mismatch"},
				{name: "wrong filename", marker: "*", hash: fmt.Sprintf("%x", sum), suffix: ".other", want: "checksum mismatch"},
				{name: "invalid marker", marker: "**", hash: fmt.Sprintf("%x", sum), want: "checksum mismatch"},
				{name: "duplicate", marker: "*", hash: fmt.Sprintf("%x", sum), duplicate: true, want: "duplicate checksum"},
			} {
				t.Run(fixture.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if strings.HasSuffix(r.URL.Path, "/SHA256SUMS") {
							line := fmt.Sprintf("%s %sa-stack_windows_amd64.zip%s\n%s %sa-stack_windows_arm64.zip%s\n", fixture.hash, fixture.marker, fixture.suffix, fixture.hash, fixture.marker, fixture.suffix)
							fmt.Fprint(w, line)
							if fixture.duplicate {
								fmt.Fprint(w, line)
							}
						} else {
							w.Write(archive)
						}
					}))
					defer server.Close()
					dir := t.TempDir()
					target := filepath.Join(dir, "a-stack.exe")
					if err := os.WriteFile(target, []byte("keep the installed binary"), 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("A_STACK_BASE_URL", server.URL)
					t.Setenv("A_STACK_VERSION", "v0.0.1")
					t.Setenv("A_STACK_INSTALL_DIR", dir)
					t.Setenv("A_STACK_NO_PATH", "1")
					out, err := exec.Command(command, "-NoProfile", "-NonInteractive", "-File", script).CombinedOutput()
					if err == nil || !strings.Contains(string(out), fixture.want) {
						t.Fatalf("unexpected installer result: %v: %s", err, out)
					}
					if data, err := os.ReadFile(target); err != nil || string(data) != "keep the installed binary" {
						t.Fatal(string(data), err)
					}
					if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
						t.Fatal("installer moved the old binary", err)
					}
				})
			}
		})
	}
}
