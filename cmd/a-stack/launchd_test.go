package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// launchdSetup is serviceSetup for macOS: an empty home, a binary in a folder
// with a space, a state directory and a fake launchctl. Each call is logged
// without "launchctl" and with gui/UID written as gui/U.
func launchdSetup(t *testing.T) (f *fakeSystemd, exe, state, file string) {
	t.Helper()
	unixOnly(t, "launchd agents hold Unix paths")
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("A_STACK_SERVICE_UNIT", "")
	exe = filepath.Join(root, "my bin", "a-stack")
	if os.MkdirAll(filepath.Dir(exe), 0o755) != nil || os.WriteFile(exe, nil, 0o755) != nil {
		t.Fatal("binary")
	}
	f = &fakeSystemd{replies: map[string]func() (string, error){
		"print gui/U/dev.a-stack": func() (string, error) { return "Could not find service", errExit },
	}}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	oldRun, oldExecutable, oldWait, oldOS := runCommand, executable, serviceWait, serviceOS
	serviceOS = "darwin"
	runCommand = func(name string, args ...string) (string, error) {
		if name != "launchctl" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		call := strings.ReplaceAll(strings.Join(args, " "), domain, "gui/U")
		f.calls = append(f.calls, call)
		if reply := f.replies[call]; reply != nil {
			return reply()
		}
		return "", nil
	}
	executable, serviceWait = func() (string, error) { return exe, nil }, 2*time.Second
	t.Cleanup(func() { runCommand, executable, serviceWait, serviceOS = oldRun, oldExecutable, oldWait, oldOS })
	return f, exe, filepath.Join(root, "state"), filepath.Join(home, "Library", "LaunchAgents", "dev.a-stack.plist")
}

func TestLaunchdPlistRoundTrip(t *testing.T) {
	for _, path := range []string{"/usr/local/bin/a-stack", "/Users/me/my bin/a-stack", `/x/<a> & "b" 'c'`} {
		plist, err := renderPlist("dev.a-stack", path, "/state & dir", "/Users/me/Library/Logs/a-stack.log")
		if err != nil {
			t.Fatal(err)
		}
		if binary, state := plistCommand(plist); binary != path || state != "/state & dir" {
			t.Fatalf("%q came back as %q and %q", path, binary, state)
		}
		// macOS checks the plist syntax too.
		if _, err := exec.LookPath("plutil"); err == nil {
			file := filepath.Join(t.TempDir(), "a.plist")
			if os.WriteFile(file, []byte(plist), 0o600) != nil {
				t.Fatal("write")
			}
			if out, err := exec.Command("plutil", "-lint", file).CombinedOutput(); err != nil {
				t.Fatalf("plutil: %s\n%s", out, plist)
			}
		}
	}
	// xml.EscapeText writes U+FFFD for these, which names another path.
	for _, path := range []string{"/x\n", "/x\xff", "/x￾"} {
		if _, err := renderPlist("dev.a-stack", path, "/s", "/l"); err == nil {
			t.Fatalf("%q reached the plist", path)
		}
	}
}

func TestLaunchdInstallStatusUninstall(t *testing.T) {
	f, exe, state, file := launchdSetup(t)
	f.replies["bootstrap gui/U "+file] = func() (string, error) {
		startTestService(t, state)
		return "", nil
	}
	out, _ := runService(t, 0, "install", "--state-dir", state)
	data, err := os.ReadFile(file)
	if binary, dir := plistCommand(string(data)); err != nil || binary != exe || dir != state || !strings.Contains(string(data), "<key>KeepAlive</key>") {
		t.Fatalf("plist: %v\n%s", err, data)
	}
	if !slices.Equal(f.calls, []string{"print gui/U/dev.a-stack", "enable gui/U/dev.a-stack", "bootstrap gui/U " + file}) {
		t.Fatalf("calls %v", f.calls)
	}
	for _, line := range []string{"Wrote " + file + ".", "It runs " + exe + " serve --state-dir " + state + ".", "launchd runs a-stack at http://127.0.0.1:"} {
		if !strings.Contains(out, line) {
			t.Fatalf("install output lacks %q:\n%s", line, out)
		}
	}

	// Again, while loaded: kickstart starts it only if it stopped.
	f.calls = nil
	f.replies["print gui/U/dev.a-stack"] = func() (string, error) {
		return "gui/501/dev.a-stack = {\n\tactive count = 1\n\tstate = running\n}", nil
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	out, _ = runService(t, 0, "install", "--state-dir", state, "--json")
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["unit_file_status"] != "unchanged" || result.Data["unit"] != "dev.a-stack" || result.Data["restart_hint"] != nil || !slices.Equal(f.calls, []string{"print gui/U/dev.a-stack", "kickstart gui/U/dev.a-stack"}) {
		t.Fatalf("second install %v %s", f.calls, out)
	}

	// A changed plist is replaced, and the loaded agent needs a reload.
	if os.WriteFile(file, []byte("<plist/>"), 0o644) != nil {
		t.Fatal("edit")
	}
	out, _ = runService(t, 0, "install", "--state-dir", state)
	if !strings.Contains(out, "Updated "+file+"; it differed from the plist") || !strings.Contains(out, "launchctl bootout gui/") {
		t.Fatalf("update output:\n%s", out)
	}

	out, _ = runService(t, 0, "status", "--state-dir", state, "--json")
	result.Data = nil
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["installed"] != true || result.Data["loaded"] != true || result.Data["active"] != "running" || result.Data["binary"] != exe || result.Data["state_dir"] != state || !strings.Contains(out, `"health":{"data":{"database_id"`) {
		t.Fatalf("status %s", out)
	}
	out, _ = runService(t, 0, "status", "--state-dir", state)
	if !strings.Contains(out, "Plist:") || !strings.Contains(out, filepath.Join("Library", "Logs", "a-stack.log")) || !strings.Contains(out, "answers at http://127.0.0.1:") {
		t.Fatalf("status text:\n%s", out)
	}

	f.calls = nil
	out, _ = runService(t, 0, "uninstall")
	if _, err := os.Stat(file); !os.IsNotExist(err) || !slices.Equal(f.calls, []string{"print gui/U/dev.a-stack", "bootout gui/U/dev.a-stack"}) || !strings.Contains(out, "removed "+file) {
		t.Fatalf("uninstall %v %v:\n%s", err, f.calls, out)
	}
	f.calls = nil
	f.replies["print gui/U/dev.a-stack"] = func() (string, error) { return "Could not find service", errExit }
	if out, _ = runService(t, 0, "uninstall"); !slices.Equal(f.calls, []string{"print gui/U/dev.a-stack"}) || !strings.Contains(out, "is not installed") {
		t.Fatalf("second uninstall %v %s", f.calls, out)
	}
}

// An agent stays loaded after its plist is gone. Uninstall unloads it, and
// status shows the plist that launchd loaded it from.
func TestLaunchdLoadedWithoutItsPlist(t *testing.T) {
	f, exe, state, file := launchdSetup(t)
	other := filepath.Join(t.TempDir(), "dev.a-stack.plist")
	plist, err := renderPlist("dev.a-stack", exe, "/other/state", "/l")
	if err != nil || os.WriteFile(other, []byte(plist), 0o600) != nil {
		t.Fatal(err)
	}
	f.replies["print gui/U/dev.a-stack"] = func() (string, error) {
		return "gui/501/dev.a-stack = {\n\tactive count = 1\n\tpath = " + other + "\n\tstate = running\n\tstdout path = /l\n}", nil
	}
	out, _ := runService(t, 0, "status", "--state-dir", state)
	for _, want := range []string{"Plist:     " + other + " (a-stack service install would write " + file + ")", "Active:    running", "State dir: /other/state", "The CLI uses " + state} {
		if !strings.Contains(out, want) {
			t.Fatalf("status lacks %q:\n%s", want, out)
		}
	}
	if binary, running := launchdProgram("dev.a-stack"); binary != exe || !running {
		t.Fatal("program", binary, running)
	}
	f.calls = nil
	out, _ = runService(t, 0, "uninstall")
	if !slices.Equal(f.calls, []string{"print gui/U/dev.a-stack", "bootout gui/U/dev.a-stack"}) || !strings.Contains(out, "unloaded it from launchd; "+file+" was already gone") {
		t.Fatalf("uninstall %v:\n%s", f.calls, out)
	}
}

func TestLaunchdInstallRefusesAForegroundServe(t *testing.T) {
	f, _, state, file := launchdSetup(t)
	startTestService(t, state)
	_, errout := runService(t, 1, "install", "--state-dir", state)
	if !strings.Contains(errout, `"code":"conflict"`) || !strings.Contains(errout, "outside launchd; press Ctrl-C in its terminal first") {
		t.Fatalf("stderr %s", errout)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) || !slices.Equal(f.calls, []string{"print gui/U/dev.a-stack"}) {
		t.Fatalf("install changed something: %v %v", err, f.calls)
	}
}

func TestLaunchdInstallFailures(t *testing.T) {
	f, _, state, file := launchdSetup(t)
	serviceWait = 0
	_, errout := runService(t, 1, "install", "--state-dir", state)
	if !strings.Contains(errout, "does not answer; see launchctl print gui/") {
		t.Fatalf("stderr %s", errout)
	}
	f.replies["bootstrap gui/U "+file] = func() (string, error) { return "Bootstrap failed: 5: Input/output error", errExit }
	if _, errout = runService(t, 1, "install", "--state-dir", state); !strings.Contains(errout, "launchctl bootstrap gui/") || !strings.Contains(errout, "Input/output error") {
		t.Fatalf("stderr %s", errout)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("an earlier plist was removed:", err)
	}
	// A new plist that launchd does not take is removed again.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, errout = runService(t, 1, "install", "--state-dir", state); !strings.Contains(errout, "Input/output error") {
		t.Fatalf("stderr %s", errout)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("a failed install left the plist:", err)
	}
}
