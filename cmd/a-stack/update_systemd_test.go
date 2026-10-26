package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/skills"
)

// Opt in only: this creates and removes one transient, differently named
// user unit. Neither the real a-stack unit nor installed binary is touched.
func TestUpdateDisposableSystemd(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("A_STACK_TEST_SYSTEMD") != "1" {
		t.Skip("set A_STACK_TEST_SYSTEMD=1 to test a disposable user unit")
	}
	release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{}}
	exe, _ := updateSetup(t, "v0.1.0", release)
	// updateSetup changes cwd; build from this source tree instead.
	_, source, _, _ := runtime.Caller(0)
	build := func(file, v string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-ldflags=-X main.version="+v, "-o", file, ".")
		cmd.Dir = filepath.Dir(source)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	build(exe, "v0.1.0")
	newBinary := filepath.Join(t.TempDir(), "new-a-stack")
	build(newBinary, "v0.2.0")
	binary, err := os.ReadFile(newBinary)
	if err != nil {
		t.Fatal(err)
	}
	release.archives["v0.2.0"] = tarGz(t, map[string]string{"a-stack": string(binary)})
	for _, name := range []string{"XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, t.TempDir())
	}
	folders, err := skillFolders(false)
	if err != nil {
		t.Fatal(err)
	}
	skillFile := filepath.Join(folders[0], "SKILL.md")
	oldSkill, err := os.ReadFile(filepath.Join(filepath.Dir(source), "testdata", "refresh", "skill-v0.1.4.md"))
	if err != nil || os.MkdirAll(folders[0], 0o700) != nil || os.WriteFile(skillFile, oldSkill, 0o644) != nil {
		t.Fatal("old installed skill fixture", err)
	}
	state := filepath.Join(t.TempDir(), "state")
	unit := fmt.Sprintf("a-stack-update-test-%d", time.Now().UnixNano())
	t.Setenv("A_STACK_SERVICE_UNIT", unit)
	oldService := updateService
	updateService = serviceProgram
	t.Cleanup(func() { updateService = oldService })
	command := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v %s", name, args, err, out)
		}
		return string(out)
	}
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "--user", "stop", unit).Run()
		_ = exec.Command("systemctl", "--user", "reset-failed", unit).Run()
	})
	command("systemd-run", "--user", "--unit="+unit, "--collect", "--property=Type=exec", exe, "serve", "--state-dir", state, "--listen", "127.0.0.1:0")
	if _, err := waitForService(state); err != nil {
		t.Fatal(err)
	}
	h, _, err := health(state)
	if err != nil || h.Version != "v0.1.0" {
		t.Fatalf("before: %v %v", h, err)
	}
	out, _ := runUpdate(t, 0)
	if !strings.Contains(out, "Restarted the service; it runs v0.2.0") || !strings.Contains(out, "Refreshed "+skillFile) {
		t.Fatal(out)
	}
	currentSkill, _ := skills.FS.ReadFile("a-stack/SKILL.md")
	if got, err := os.ReadFile(skillFile); err != nil || string(got) != string(currentSkill) {
		t.Fatal("replacement binary did not refresh the skill", err)
	}
	h, _, err = health(state)
	if err != nil || h.Version != "v0.2.0" {
		t.Fatalf("after: %v %v", h, err)
	}
	t.Logf("%s on %s: v0.1.0 -> %s; unit removed at cleanup", unit, state, h.Version)
}
