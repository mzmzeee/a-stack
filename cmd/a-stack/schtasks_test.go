package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// fakeTasks is Task Scheduler for one task: /Create stores the XML that
// schtasks would read, and /Query prints it back.
type fakeTasks struct {
	calls   []string
	task    string
	replies map[string]func() (string, error)
}

// schtasksSetup is serviceSetup for Windows: a binary in a folder with a
// space, a state directory and a fake schtasks. Calls are logged without
// the name of the temporary task file.
func schtasksSetup(t *testing.T) (f *fakeTasks, exe, state string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("A_STACK_SERVICE_UNIT", "")
	exe = filepath.Join(root, "my bin", "a-stack.exe")
	if os.MkdirAll(filepath.Dir(exe), 0o755) != nil || os.WriteFile(exe, nil, 0o755) != nil {
		t.Fatal("binary")
	}
	f = &fakeTasks{replies: map[string]func() (string, error){}}
	oldRun, oldExecutable, oldWait, oldOS, oldUser := runCommand, executable, serviceWait, serviceOS, taskUser
	serviceOS = "windows"
	taskUser = func() (string, error) { return `DESKTOP\me & you`, nil }
	runCommand = func(name string, args ...string) (string, error) {
		if name != "schtasks" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		if args[0] == "/Create" {
			data, err := os.ReadFile(args[2])
			if err != nil || len(data) < 2 || data[0] != 0xff || data[1] != 0xfe || len(data)%2 != 0 {
				t.Fatalf("the task file is not UTF-16 with a byte order mark: %v", err)
			}
			units := make([]uint16, 0, len(data)/2-1)
			for i := 2; i < len(data); i += 2 {
				units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
			}
			f.task = string(utf16.Decode(units))
			args[2] = "FILE"
		}
		call := strings.Join(args, " ")
		f.calls = append(f.calls, call)
		if reply := f.replies[call]; reply != nil {
			return reply()
		}
		switch args[0] {
		case "/Query":
			if f.task == "" {
				return "ERROR: The system cannot find the file specified.", errExit
			}
			return f.task, nil
		case "/Delete":
			f.task = ""
		}
		return "", nil
	}
	executable, serviceWait = func() (string, error) { return exe, nil }, 2*time.Second
	t.Cleanup(func() {
		runCommand, executable, serviceWait, serviceOS, taskUser = oldRun, oldExecutable, oldWait, oldOS, oldUser
	})
	return f, exe, filepath.Join(root, "state dir")
}

func TestWindowsArgRoundTrip(t *testing.T) {
	for _, s := range []string{`C:\Users\me\AppData\Local\a-stack`, `C:\my state\`, `D:\a "b"\c`, `x\\`, ""} {
		if got := unquoteWindowsArg(windowsArg(s)); got != s {
			t.Errorf("%q came back as %q (quoted %s)", s, got, windowsArg(s))
		}
	}
	if windowsArg(`C:\plain`) != `C:\plain` || windowsArg(`C:\my dir\`) != `"C:\my dir\\"` {
		t.Fatal(windowsArg(`C:\my dir\`))
	}
}

func TestTaskXML(t *testing.T) {
	task, err := renderTask(`DESKTOP\me`, `C:\Program Files\<a> & b\a-stack.exe`, `C:\my state`)
	if err != nil {
		t.Fatal(err)
	}
	// The settings that keep it running: a repetition every minute that
	// skips while serve runs, no time limit, no stop on battery, no admin.
	for _, want := range []string{"<LogonTrigger>", "<UserId>DESKTOP\\me</UserId>", "<Interval>PT1M</Interval>", "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>", "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>", "<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>", "<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>", "<RunLevel>LeastPrivilege</RunLevel>", "<LogonType>InteractiveToken</LogonType>", "&lt;a&gt; &amp; b", `<Arguments>serve --state-dir &#34;C:\my state&#34;</Arguments>`} {
		if !strings.Contains(task, want) {
			t.Errorf("the task lacks %s", want)
		}
	}
	if binary, state, enabled := taskCommand(task); binary != `C:\Program Files\<a> & b\a-stack.exe` || state != `C:\my state` || !enabled {
		t.Fatalf("task command %q %q %v", binary, state, enabled)
	}
	if _, err := renderTask("me", "C:\\a\nb", "C:\\s"); err == nil {
		t.Fatal("a newline reached the task")
	}
	if utf16File("a\n")[0] != 0xff || string(utf16File("a\n")[2:]) != "a\x00\r\x00\n\x00" {
		t.Fatal("utf16File")
	}
}

func TestSchtasksInstallStatusUninstall(t *testing.T) {
	f, exe, state := schtasksSetup(t)
	f.replies["/Run /TN a-stack"] = func() (string, error) {
		startTestService(t, state)
		return "SUCCESS: Attempted to run the scheduled task \"a-stack\".", nil
	}
	out, _ := runService(t, 0, "install", "--state-dir", state)
	if !slices.Equal(f.calls, []string{"/Query /TN a-stack /XML", "/Create /XML FILE /TN a-stack /F", "/Run /TN a-stack"}) {
		t.Fatalf("calls %v", f.calls)
	}
	if binary, dir, _ := taskCommand(f.task); binary != exe || dir != state || !strings.Contains(f.task, `<UserId>DESKTOP\me &amp; you</UserId>`) {
		t.Fatalf("task:\n%s", f.task)
	}
	for _, line := range []string{"Created the Task Scheduler task a-stack.", "It runs " + exe + " serve --state-dir " + state + ".", "Task Scheduler runs a-stack at http://127.0.0.1:"} {
		if !strings.Contains(out, line) {
			t.Fatalf("install output lacks %q:\n%s", line, out)
		}
	}

	// Again, while it runs: nothing changes.
	f.calls = nil
	var result struct {
		Data map[string]any `json:"data"`
	}
	out, _ = runService(t, 0, "install", "--state-dir", state, "--json")
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["unit_file_status"] != "unchanged" || result.Data["unit"] != "a-stack" || result.Data["unit_file"] != nil || result.Data["restart_hint"] != nil || !slices.Equal(f.calls, []string{"/Query /TN a-stack /XML"}) {
		t.Fatalf("second install %v %s", f.calls, out)
	}

	// A changed task is replaced, and the running serve needs a restart.
	f.task = strings.Replace(f.task, exe, `C:\old\a-stack.exe`, 1)
	f.calls = nil
	out, _ = runService(t, 0, "install", "--state-dir", state)
	if !strings.Contains(out, "Updated the Task Scheduler task a-stack; it differed") || !strings.Contains(out, "To use the new one, run: ") || !slices.Equal(f.calls, []string{"/Query /TN a-stack /XML", "/Create /XML FILE /TN a-stack /F"}) {
		t.Fatalf("update %v:\n%s", f.calls, out)
	}

	out, _ = runService(t, 0, "status", "--state-dir", state, "--json")
	result.Data = nil
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["installed"] != true || result.Data["enabled"] != true || result.Data["binary"] != exe || result.Data["state_dir"] != state || result.Data["log"] != state+".log" || !strings.Contains(out, `"health":{"data":{"database_id"`) {
		t.Fatalf("status %s", out)
	}
	out, _ = runService(t, 0, "status", "--state-dir", state)
	if !strings.Contains(out, "Task:      a-stack") || !strings.Contains(out, "Log:       "+state+".log") || !strings.Contains(out, "answers at http://127.0.0.1:") {
		t.Fatalf("status text:\n%s", out)
	}

	f.calls = nil
	out, _ = runService(t, 0, "uninstall")
	if f.task != "" || !slices.Equal(f.calls, []string{"/Query /TN a-stack /XML", "/End /TN a-stack", "/Delete /TN a-stack /F"}) || !strings.Contains(out, "Stopped and removed the task a-stack.") {
		t.Fatalf("uninstall %v:\n%s", f.calls, out)
	}
	f.calls = nil
	if out, _ = runService(t, 0, "uninstall"); len(f.calls) != 1 || !strings.Contains(out, "is not installed") {
		t.Fatalf("second uninstall %v %s", f.calls, out)
	}
	if out, _ = runService(t, 0, "status", "--state-dir", state); !strings.Contains(out, "not installed; run a-stack service install") {
		t.Fatalf("status without a task:\n%s", out)
	}
}

func TestSchtasksInstallRefusesAForegroundServe(t *testing.T) {
	f, _, state := schtasksSetup(t)
	startTestService(t, state)
	_, errout := runService(t, 1, "install", "--state-dir", state)
	if !strings.Contains(errout, `"code":"conflict"`) || !strings.Contains(errout, "outside Task Scheduler; press Ctrl-C in its terminal first") {
		t.Fatalf("stderr %s", errout)
	}
	if f.task != "" || !slices.Equal(f.calls, []string{"/Query /TN a-stack /XML"}) {
		t.Fatalf("install changed something: %v", f.calls)
	}
}

func TestSchtasksInstallFailures(t *testing.T) {
	f, _, state := schtasksSetup(t)
	serviceWait = 0
	_, errout := runService(t, 1, "install", "--state-dir", state)
	if !strings.Contains(errout, "does not answer; see schtasks /Query /TN a-stack /V /FO LIST and "+state+".log") {
		t.Fatalf("stderr %s", errout)
	}
	f.task = ""
	f.replies["/Create /XML FILE /TN a-stack /F"] = func() (string, error) { return "ERROR: Access is denied.", errExit }
	if _, errout = runService(t, 1, "install", "--state-dir", state); !strings.Contains(errout, "schtasks /Create /TN a-stack failed: ERROR: Access is denied.") {
		t.Fatalf("stderr %s", errout)
	}
	// A new task that cannot run is removed again.
	f.replies["/Create /XML FILE /TN a-stack /F"] = nil
	f.replies["/Run /TN a-stack"] = func() (string, error) { return "ERROR: Access is denied.", errExit }
	f.task, f.calls = "", nil
	if _, errout = runService(t, 1, "install", "--state-dir", state); !strings.Contains(errout, "schtasks /Run /TN a-stack failed") || f.task != "" || !slices.Contains(f.calls, "/Delete /TN a-stack /F") {
		t.Fatalf("stderr %s, calls %v", errout, f.calls)
	}
}
