package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/contrib/systemd"
	"github.com/mzmzeee/a-stack/internal/stack"
)

// fakeSystemd stands in for systemctl and loginctl. Each call is logged
// without the leading "systemctl --user"; replies maps a call to a function
// that answers it, and a call without a reply succeeds with no output.
type fakeSystemd struct {
	calls   []string
	replies map[string]func() (string, error)
}

var errExit = errors.New("exit status 3")

// serviceSetup gives the test an empty home and config folder, a binary in a
// folder with a space, a state directory and a fake systemd.
func serviceSetup(t *testing.T) (f *fakeSystemd, exe, state, file string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("A_STACK_SERVICE_UNIT", "")
	exe = filepath.Join(root, "my bin", "a-stack")
	if os.MkdirAll(filepath.Dir(exe), 0o755) != nil || os.WriteFile(exe, nil, 0o755) != nil {
		t.Fatal("binary")
	}
	f = &fakeSystemd{replies: map[string]func() (string, error){
		"is-active --quiet a-stack": func() (string, error) { return "", errExit },
		"is-active a-stack":         func() (string, error) { return "inactive", errExit },
		"is-enabled a-stack":        func() (string, error) { return "disabled", errExit },
		"loginctl show-user":        func() (string, error) { return "Linger=no", nil },
	}}
	oldRun, oldExecutable, oldWait := runCommand, executable, serviceWait
	runCommand = func(name string, args ...string) (string, error) {
		var call string
		switch {
		case name == "systemctl" && args[0] == "--user":
			call = strings.Join(args[1:], " ")
		case name == "loginctl":
			call = "loginctl " + args[0]
		default:
			t.Fatalf("unexpected command %s %v", name, args)
		}
		f.calls = append(f.calls, call)
		if reply := f.replies[call]; reply != nil {
			return reply()
		}
		return "", nil
	}
	executable, serviceWait = func() (string, error) { return exe, nil }, 2*time.Second
	t.Cleanup(func() { runCommand, executable, serviceWait = oldRun, oldExecutable, oldWait })
	return f, exe, filepath.Join(root, "state"), filepath.Join(root, "config", "systemd", "user", "a-stack.service")
}

// startTestService runs a service on state until the test ends.
func startTestService(t *testing.T, state string) {
	t.Helper()
	ctx := context.Background()
	service, err := stack.Start(ctx, state, "")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- service.Serve() }()
	t.Cleanup(func() {
		service.Close(ctx)
		<-done
	})
}

func runService(t *testing.T, wantCode int, args ...string) (string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	if code := run(append([]string{"service"}, args...), &out, &errout); code != wantCode {
		t.Fatalf("service %v: exit %d, want %d; stdout=%s stderr=%s", args, code, wantCode, out.String(), errout.String())
	}
	return out.String(), errout.String()
}

func TestServiceUnitEmbedMatchesRepository(t *testing.T) {
	data, err := os.ReadFile("../../contrib/systemd/a-stack.service")
	if err != nil || string(data) != systemd.Unit || strings.Count(systemd.Unit, defaultExecStart) != 1 {
		t.Fatal("the embedded unit differs from contrib/systemd/a-stack.service or lost its ExecStart line")
	}
	// The default paths keep the unit as it is.
	if unit, err := renderUnit("/home/u/.local/bin/a-stack", "/home/u/.local/state/a-stack", "/home/u"); err != nil || unit != systemd.Unit {
		t.Fatalf("default unit changed: %v", err)
	}
}

func TestServiceUnitWordsRoundTrip(t *testing.T) {
	home := "/home/u"
	for _, path := range []string{"/usr/bin/a-stack", "/opt/my bin/a-stack", `/x/100%h "$HOME" \n`, "/home/u/.local/bin/a-stack"} {
		unit, err := renderUnit(path, "/srv/state dir", home)
		if err != nil {
			t.Fatal(err)
		}
		if binary, state := unitCommand(unit, home); binary != path || state != "/srv/state dir" {
			t.Fatalf("%q came back as %q and %q", path, binary, state)
		}
	}
	if _, err := renderUnit("/x\n[Service]", "/s", home); err == nil {
		t.Fatal("a newline in a path reached the unit")
	}
}

func TestServiceInstallStatusUninstall(t *testing.T) {
	f, exe, state, file := serviceSetup(t)
	f.replies["enable --now a-stack"] = func() (string, error) {
		startTestService(t, state)
		return "", nil
	}
	out, _ := runService(t, 0, "install", "--state-dir", state)
	want, _ := renderUnit(exe, state, os.Getenv("HOME"))
	if data, err := os.ReadFile(file); err != nil || string(data) != want || !strings.Contains(want, `ExecStart="`+exe+`" serve --state-dir `+state+"\n") {
		t.Fatalf("unit file: %v\n%s", err, data)
	}
	if info, _ := os.Stat(file); info.Mode().Perm() != 0o644 {
		t.Fatalf("unit mode %v", info.Mode())
	}
	if !slices.Equal(f.calls, []string{"is-active --quiet a-stack", "daemon-reload", "enable --now a-stack", "loginctl show-user"}) {
		t.Fatalf("calls %v", f.calls)
	}
	for _, line := range []string{"Wrote " + file + ".", "It runs " + exe + " serve --state-dir " + state + ".", "systemd runs a-stack at http://127.0.0.1:", "loginctl enable-linger "} {
		if !strings.Contains(out, line) {
			t.Fatalf("install output lacks %q:\n%s", line, out)
		}
	}

	// Again, with the unit active: nothing changes, and no foreground check.
	f.replies["is-active --quiet a-stack"] = func() (string, error) { return "", nil }
	f.replies["is-active a-stack"] = func() (string, error) { return "active", nil }
	f.replies["is-enabled a-stack"] = func() (string, error) { return "enabled", nil }
	f.replies["enable --now a-stack"] = nil
	f.replies["loginctl show-user"] = func() (string, error) { return "Linger=yes", nil }
	var result struct {
		Data map[string]any `json:"data"`
	}
	out, _ = runService(t, 0, "install", "--state-dir", state, "--json")
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["unit_file_status"] != "unchanged" || result.Data["binary"] != exe || result.Data["linger"] != "yes" || result.Data["restart_hint"] != nil {
		t.Fatalf("second install %s", out)
	}

	// A changed unit file is replaced, and the running service needs a restart.
	if os.WriteFile(file, []byte("[Service]\nExecStart=/old/a-stack serve\n"), 0o644) != nil {
		t.Fatal("edit")
	}
	out, _ = runService(t, 0, "install", "--state-dir", state)
	if data, _ := os.ReadFile(file); string(data) != want || !strings.Contains(out, "Updated "+file+"; it differed") || !strings.Contains(out, "systemctl --user restart a-stack") || strings.Contains(out, "enable-linger") {
		t.Fatalf("update output:\n%s", out)
	}

	out, _ = runService(t, 0, "status", "--state-dir", state, "--json")
	result.Data = nil
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["installed"] != true || result.Data["enabled"] != "enabled" || result.Data["active"] != "active" || result.Data["binary"] != exe || result.Data["state_dir"] != state || !strings.Contains(out, `"health":{"data":{"database_id"`) {
		t.Fatalf("status %s", out)
	}
	out, _ = runService(t, 0, "status", "--state-dir", state)
	if !strings.Contains(out, "Unit file:") || !strings.Contains(out, file) || !strings.Contains(out, "answers at http://127.0.0.1:") {
		t.Fatalf("status text:\n%s", out)
	}

	f.calls = nil
	out, _ = runService(t, 0, "uninstall")
	if _, err := os.Stat(file); !os.IsNotExist(err) || !slices.Equal(f.calls, []string{"disable --now a-stack", "daemon-reload"}) || !strings.Contains(out, "removed "+file) {
		t.Fatalf("uninstall %v %v:\n%s", err, f.calls, out)
	}
	if _, err := os.Stat(filepath.Join(state, "state.db")); err != nil {
		t.Fatal("uninstall touched the state directory")
	}
	f.calls = nil
	out, _ = runService(t, 0, "uninstall", "--json")
	if len(f.calls) != 0 || out != `{"data":{"removed":false,"unit":"a-stack","unit_file":"`+file+`"}}`+"\n" {
		t.Fatalf("second uninstall %v %s", f.calls, out)
	}
	out, _ = runService(t, 0, "status", "--state-dir", state)
	if !strings.Contains(out, "not installed; run a-stack service install") {
		t.Fatalf("status without unit:\n%s", out)
	}
}

func TestServiceInstallRefusesAForegroundServe(t *testing.T) {
	f, _, state, file := serviceSetup(t)
	startTestService(t, state)
	_, errout := runService(t, 1, "install", "--state-dir", state)
	if !strings.Contains(errout, `"code":"conflict"`) || !strings.Contains(errout, "press Ctrl-C in its terminal first") {
		t.Fatalf("stderr %s", errout)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) || !slices.Equal(f.calls, []string{"is-active --quiet a-stack"}) {
		t.Fatalf("install changed something: %v %v", err, f.calls)
	}
}

func TestServiceInstallFailures(t *testing.T) {
	f, _, state, _ := serviceSetup(t)
	serviceWait = 0
	// The unit starts but the service never answers.
	_, errout := runService(t, 1, "install", "--state-dir", state)
	if !strings.Contains(errout, "does not answer; see systemctl --user status a-stack") {
		t.Fatalf("stderr %s", errout)
	}
	f.replies["enable --now a-stack"] = func() (string, error) { return "Failed to enable unit", errExit }
	if _, errout = runService(t, 1, "install", "--state-dir", state); !strings.Contains(errout, "systemctl --user enable --now a-stack failed: Failed to enable unit") {
		t.Fatalf("stderr %s", errout)
	}
	f.replies["disable --now a-stack"] = func() (string, error) { return "Access denied", errExit }
	if _, errout = runService(t, 1, "uninstall"); !strings.Contains(errout, "Access denied") {
		t.Fatalf("stderr %s", errout)
	}
}

func TestServiceUnitNameAndConfigFolder(t *testing.T) {
	f, _, state, _ := serviceSetup(t)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("A_STACK_SERVICE_UNIT", "a-stack-try")
	out, _ := runService(t, 0, "status", "--state-dir", state, "--json")
	if !strings.Contains(out, `"unit_file":"`+filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", "a-stack-try.service")+`"`) || !slices.Contains(f.calls, "is-active a-stack-try") {
		t.Fatalf("status %s %v", out, f.calls)
	}
	t.Setenv("A_STACK_SERVICE_UNIT", "../a-stack")
	runService(t, 2, "status")
	t.Setenv("A_STACK_SERVICE_UNIT", "")
	t.Setenv("XDG_CONFIG_HOME", "config")
	runService(t, 2, "status")
	runService(t, 2, "uninstall", "--state-dir", state)
	runService(t, 2, "restart")
}
