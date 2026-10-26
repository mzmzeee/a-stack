package main

import (
	"encoding/json"
	"encoding/xml"
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
	oldRun, oldExecutable, oldWait, oldOS, oldUser, oldName := runCommand, executable, serviceWait, serviceOS, taskUser, taskName
	serviceOS = "windows"
	taskName = func() (string, error) { return "a-stack-S-1-5-21-1234", nil }
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
		runCommand, executable, serviceWait, serviceOS, taskUser, taskName = oldRun, oldExecutable, oldWait, oldOS, oldUser, oldName
	})
	return f, exe, filepath.Join(root, "state dir")
}

func TestWindowsArgRoundTrip(t *testing.T) {
	for _, s := range []string{`C:\Users\me\AppData\Local\a-stack`, `C:\my state\`, `D:\a "b"\c`, `a\"b`, `a\\"b`, `x\\`, ""} {
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
	for _, want := range []string{"<LogonTrigger>", "<UserId>DESKTOP\\me</UserId>", "<Interval>PT1M</Interval>", "<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>", "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>", "<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>", "<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>", "<RunLevel>LeastPrivilege</RunLevel>", "<LogonType>InteractiveToken</LogonType>", `<Command>&#34;C:\Program Files\&lt;a&gt; &amp; b\a-stack.exe&#34;</Command>`, `<Arguments>serve --state-dir &#34;C:\my state&#34;</Arguments>`} {
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

func TestTaskXMLPreservesPercentPaths(t *testing.T) {
	for _, paths := range [][2]string{
		{`C:\bin%PATH%\a-stack.exe`, `C:\state`},
		{`C:\bin\a-stack.exe`, `C:\state%TEMP%\`},
		{`C:\100%\a-stack.exe`, `C:\state`},
		{`C:\O'Brien %PATH%\a-stack.exe`, `C:\état & 'quoted' %TEMP%\`},
	} {
		task, err := renderTask("me", paths[0], paths[1])
		if err != nil {
			t.Fatal(err)
		}
		var action struct {
			Command string `xml:"Actions>Exec>Command"`
			Args    string `xml:"Actions>Exec>Arguments"`
		}
		if err := xml.Unmarshal([]byte(task[strings.Index(task, "<Task"):]), &action); err != nil {
			t.Fatal(err)
		}
		if action.Command != taskPowerShell || strings.ContainsAny(action.Args, "%") {
			t.Fatalf("literal path can be expanded: %q %q", action.Command, action.Args)
		}
		if binary, state, enabled := taskCommand(task); binary != paths[0] || state != paths[1] || !enabled {
			t.Fatalf("lost literal paths: %q %q %v", binary, state, enabled)
		}
	}
	for _, bad := range []string{taskEncodedPrefix + "!", taskEncodedPrefix + "YQ==", "-EncodedCommand "} {
		if exe, args := decodeTaskArguments(bad); exe != "" || args != "" {
			t.Fatal("malformed action accepted")
		}
	}
	if _, err := renderTask("me", `C:\%PATH%\a-stack.exe`, "C:\\bad\nstate"); err == nil {
		t.Fatal("encoding hid a control character")
	}
}

func TestSchtasksReplacesUnescapedPercentAction(t *testing.T) {
	f, exe, state := schtasksSetup(t)
	state = filepath.Join(state, "%PATH%")
	startTestService(t, state)
	// A task written by an older a-stack, with correct but unsafe literal paths.
	var command, args strings.Builder
	_ = xml.EscapeText(&command, []byte(windowsArg(exe)))
	_ = xml.EscapeText(&args, []byte("serve --state-dir "+windowsArg(state)))
	f.task = `<Task><Settings><Enabled>true</Enabled></Settings><Actions><Exec><Command>` + command.String() + `</Command><Arguments>` + args.String() + `</Arguments></Exec></Actions></Task>`
	out, _ := runService(t, 0, "install", "--state-dir", state)
	if !strings.Contains(out, "Updated the Task Scheduler task") || !strings.Contains(f.task, taskEncodedPrefix) {
		t.Fatal("unsafe old action kept", out)
	}
	f.calls = nil
	out, _ = runService(t, 0, "install", "--state-dir", state)
	if !strings.Contains(out, "Kept the Task Scheduler task") || len(f.calls) != 1 {
		t.Fatal("encoded action was not idempotent", out, f.calls)
	}
}

func TestSchtasksInstallStatusUninstall(t *testing.T) {
	f, exe, state := schtasksSetup(t)
	f.replies["/Run /TN a-stack-S-1-5-21-1234"] = func() (string, error) {
		startTestService(t, state)
		return "SUCCESS: Attempted to run the scheduled task \"a-stack\".", nil
	}
	out, _ := runService(t, 0, "install", "--state-dir", state)
	if !slices.Equal(f.calls, []string{"/Query /TN a-stack-S-1-5-21-1234 /XML", "/Create /XML FILE /TN a-stack-S-1-5-21-1234 /F", "/Run /TN a-stack-S-1-5-21-1234"}) {
		t.Fatalf("calls %v", f.calls)
	}
	if binary, dir, _ := taskCommand(f.task); binary != exe || dir != state || !strings.Contains(f.task, `<UserId>DESKTOP\me &amp; you</UserId>`) {
		t.Fatalf("task:\n%s", f.task)
	}
	for _, line := range []string{"Created the Task Scheduler task a-stack-S-1-5-21-1234.", "It runs " + exe + " serve --state-dir " + state + ".", "Task Scheduler runs a-stack at http://127.0.0.1:"} {
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
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["unit_file_status"] != "unchanged" || result.Data["unit"] != "a-stack-S-1-5-21-1234" || result.Data["unit_file"] != nil || result.Data["restart_hint"] != nil || !slices.Equal(f.calls, []string{"/Query /TN a-stack-S-1-5-21-1234 /XML"}) {
		t.Fatalf("second install %v %s", f.calls, out)
	}

	// A changed task is replaced, and the running serve needs a restart.
	f.task = strings.Replace(f.task, exe, `C:\old\a-stack.exe`, 1)
	f.calls = nil
	out, _ = runService(t, 0, "install", "--state-dir", state)
	if !strings.Contains(out, "Updated the Task Scheduler task a-stack-S-1-5-21-1234; it differed") || !strings.Contains(out, "To use the new one, run: ") || !slices.Equal(f.calls, []string{"/Query /TN a-stack-S-1-5-21-1234 /XML", "/Create /XML FILE /TN a-stack-S-1-5-21-1234 /F"}) {
		t.Fatalf("update %v:\n%s", f.calls, out)
	}

	out, _ = runService(t, 0, "status", "--state-dir", state, "--json")
	result.Data = nil
	if json.Unmarshal([]byte(out), &result) != nil || result.Data["installed"] != true || result.Data["enabled"] != true || result.Data["binary"] != exe || result.Data["state_dir"] != state || result.Data["log"] != serviceLog(state) || !strings.Contains(out, `"health":{"data":{"database_id"`) {
		t.Fatalf("status %s", out)
	}
	out, _ = runService(t, 0, "status", "--state-dir", state)
	if !strings.Contains(out, "Task:      a-stack-S-1-5-21-1234") || !strings.Contains(out, "Log:       "+serviceLog(state)) || !strings.Contains(out, "answers at http://127.0.0.1:") {
		t.Fatalf("status text:\n%s", out)
	}

	f.calls = nil
	out, _ = runService(t, 0, "uninstall")
	if f.task != "" || !slices.Equal(f.calls, []string{"/Query /TN a-stack-S-1-5-21-1234 /XML", "/End /TN a-stack-S-1-5-21-1234", "/Delete /TN a-stack-S-1-5-21-1234 /F"}) || !strings.Contains(out, "Stopped and removed the task a-stack-S-1-5-21-1234.") {
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
	if f.task != "" || !slices.Equal(f.calls, []string{"/Query /TN a-stack-S-1-5-21-1234 /XML"}) {
		t.Fatalf("install changed something: %v", f.calls)
	}
}

func TestSchtasksInstallFailures(t *testing.T) {
	f, _, state := schtasksSetup(t)
	serviceWait = 0
	_, errout := runService(t, 1, "install", "--state-dir", state)
	var failure struct{ Error struct{ Message string } }
	if json.Unmarshal([]byte(errout), &failure) != nil || !strings.Contains(failure.Error.Message, "does not answer; see schtasks /Query /TN a-stack-S-1-5-21-1234 /V /FO LIST and "+serviceLog(state)) {
		t.Fatalf("stderr %s", errout)
	}
	f.task = ""
	f.replies["/Create /XML FILE /TN a-stack-S-1-5-21-1234 /F"] = func() (string, error) { return "ERROR: Access is denied.", errExit }
	if _, errout = runService(t, 1, "install", "--state-dir", state); !strings.Contains(errout, "schtasks /Create /TN a-stack-S-1-5-21-1234 failed: ERROR: Access is denied.") {
		t.Fatalf("stderr %s", errout)
	}
	// A new task that cannot run is removed again.
	f.replies["/Create /XML FILE /TN a-stack-S-1-5-21-1234 /F"] = nil
	f.replies["/Run /TN a-stack-S-1-5-21-1234"] = func() (string, error) { return "ERROR: Access is denied.", errExit }
	f.task, f.calls = "", nil
	if _, errout = runService(t, 1, "install", "--state-dir", state); !strings.Contains(errout, "schtasks /Run /TN a-stack-S-1-5-21-1234 failed") || f.task != "" || !slices.Contains(f.calls, "/Delete /TN a-stack-S-1-5-21-1234 /F") {
		t.Fatalf("stderr %s, calls %v", errout, f.calls)
	}
}

func TestSchtasksAccountLookupFailure(t *testing.T) {
	f, _, _ := schtasksSetup(t)
	taskName = func() (string, error) { return "", errExit }
	if _, err := unitName(); err == nil {
		t.Fatal("account lookup failure was ignored")
	}
	if len(f.calls) != 0 {
		t.Fatal("queried another account's task", f.calls)
	}
}

func TestSchtasksPreservesScoopCurrentPath(t *testing.T) {
	_, exe, _ := schtasksSetup(t)
	root := filepath.Join(filepath.Dir(exe), "scoop", "apps", "a-stack")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "current")
	if err := os.Symlink(filepath.Dir(exe), current); err != nil {
		t.Skip(err)
	}
	shimTarget := filepath.Join(current, filepath.Base(exe))
	executable = func() (string, error) { return shimTarget, nil }
	if got, err := serviceBinary(); err != nil || got != shimTarget {
		t.Fatal(got, err)
	}
}
