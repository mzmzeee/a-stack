package main

import (
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// On Windows, a-stack service keeps the service running with a Task
// Scheduler task for the current user, which needs no administrator. It
// starts at logon, and the task runs it again every minute; while serve
// runs, the new run is skipped (IgnoreNew), so a serve that stopped starts
// again within a minute. It never times out and keeps running on battery.
// serve sees that Windows made a console just for it, closes it and writes
// its output to the log inside the private state directory (see detachConsole).

// taskUser is the account the task runs as. Tests replace it.
var taskUser = func() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

func schtasks(args ...string) (string, error) { return runCommand("schtasks", args...) }

func schtasksFailed(step, out string) error {
	return &stack.Error{Code: "integration_unavailable", Message: "schtasks " + step + " failed: " + printable(out), Retryable: true}
}

// serviceLog is the file that a serve run by the task writes its output to:
// inside the state directory, %LOCALAPPDATA%\a-stack\service.log by default.
func serviceLog(state string) string { return filepath.Join(state, "service.log") }

// windowsArg quotes s as one argument of a Windows command line.
func windowsArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, c := range []byte(s) {
		switch c {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}
		b.WriteByte(c)
	}
	b.WriteString(strings.Repeat(`\`, slashes))
	b.WriteByte('"')
	return b.String()
}

// unquoteWindowsArg undoes windowsArg.
func unquoteWindowsArg(s string) string {
	inner, ok := strings.CutPrefix(s, `"`)
	if !ok {
		return s
	}
	inner, _ = strings.CutSuffix(inner, `"`)
	var b strings.Builder
	slashes := 0
	for _, c := range []byte(inner) {
		switch c {
		case '\\':
			slashes++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes/2))
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
		}
		slashes = 0
		b.WriteByte(c)
	}
	b.WriteString(strings.Repeat(`\`, slashes/2))
	return b.String()
}

const taskTemplate = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.4" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>a-stack serve: the local a-stack service. Made by a-stack service install.</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>%[1]s</UserId>
      <Repetition>
        <Interval>PT1M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%[1]s</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>%[2]s</Command>
      <Arguments>%[3]s</Arguments>
    </Exec>
  </Actions>
</Task>
`

const taskPowerShell = `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`
const taskEncodedPrefix = "-NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand "

func powershellLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Task Scheduler expands %VAR% in both Command and Arguments, even inside
// quotes. Only paths containing % need a wrapper. Base64 hides those paths
// from expansion; Start-Process waits and preserves the service's exit code,
// with a separate hidden console so serve still detaches and writes its log.
func encodedTaskArguments(exe, args string) string {
	script := "$p = Start-Process -FilePath " + powershellLiteral(exe) + " -ArgumentList " + powershellLiteral(args) + " -WindowStyle Hidden -PassThru -Wait -ErrorAction Stop; exit $p.ExitCode"
	return taskEncodedPrefix + base64.StdEncoding.EncodeToString(utf16File(script)[2:])
}

var encodedTaskScript = regexp.MustCompile(`^\$p = Start-Process -FilePath '((?:[^']|'')*)' -ArgumentList '((?:[^']|'')*)' -WindowStyle Hidden -PassThru -Wait -ErrorAction Stop; exit \$p.ExitCode$`)

func decodeTaskArguments(args string) (exe, command string) {
	encoded, ok := strings.CutPrefix(args, taskEncodedPrefix)
	if !ok {
		return "", ""
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data)%2 != 0 {
		return "", ""
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
	}
	parts := encodedTaskScript.FindStringSubmatch(string(utf16.Decode(units)))
	if parts == nil {
		return "", ""
	}
	return strings.ReplaceAll(parts[1], "''", "'"), strings.ReplaceAll(parts[2], "''", "'")
}

// renderTask is the task that runs exe serve --state-dir state as account.
func renderTask(account, exe, state string) (string, error) {
	for _, v := range []string{account, exe, state} {
		if strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return "", invalid("cannot write a value with control characters into the task: " + printable(v))
		}
	}
	command, args := windowsArg(exe), "serve --state-dir "+windowsArg(state)
	if strings.ContainsAny(exe+state, "%") {
		command, args = taskPowerShell, encodedTaskArguments(exe, args)
	}
	values := []string{account, command, args}
	for i, v := range values {
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(v))
		values[i] = b.String()
	}
	return fmt.Sprintf(taskTemplate, values[0], values[1], values[2]), nil
}

// utf16File is text as UTF-16 with a byte order mark, as schtasks /XML
// reads it.
func utf16File(text string) []byte {
	units := utf16.Encode([]rune(strings.ReplaceAll(text, "\n", "\r\n")))
	b := []byte{0xff, 0xfe}
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

// taskCommand returns the binary, the --state-dir and whether the task is
// enabled, from a task's XML as schtasks /Query /XML prints it.
func taskCommand(content string) (binary, state string, enabled bool) {
	// The XML declaration names UTF-16, but the text has already been
	// decoded; encoding/xml would refuse it.
	if i := strings.Index(content, "<Task"); i >= 0 {
		content = content[i:]
	}
	var task struct {
		Enabled string `xml:"Settings>Enabled"`
		Exec    struct {
			Command   string
			Arguments string
		} `xml:"Actions>Exec"`
	}
	if xml.Unmarshal([]byte(content), &task) != nil {
		return "", "", false
	}
	binary, args := unquoteWindowsArg(task.Exec.Command), task.Exec.Arguments
	if strings.EqualFold(binary, taskPowerShell) {
		binary, args = decodeTaskArguments(args)
	}
	if rest, ok := strings.CutPrefix(args, "serve --state-dir "); ok {
		state = unquoteWindowsArg(rest)
	}
	return binary, state, task.Enabled != "false"
}

// queryTask returns the task's XML, or ok false when there is no task.
func queryTask(name string) (content string, ok bool) {
	out, err := schtasks("/Query", "/TN", name, "/XML")
	if strings.Contains(out, "\x00") { // UTF-16, when not written to a console
		units := make([]uint16, 0, len(out)/2)
		for i := 0; i+1 < len(out); i += 2 {
			units = append(units, uint16(out[i])|uint16(out[i+1])<<8)
		}
		out = strings.TrimPrefix(string(utf16.Decode(units)), "\ufeff")
	}
	return out, err == nil && strings.Contains(out, "<Task")
}

// taskRunning uses the numeric Task Scheduler state, not the localized
// status text from schtasks. A stopped installed task must not be started
// by update.
func taskRunning(name string) bool {
	out, err := runCommand("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "[int](Get-ScheduledTask -TaskName '"+name+"').State")
	return err == nil && strings.TrimSpace(out) == "4"
}

// schtasksCommand runs service install, uninstall or status with Task
// Scheduler.
func schtasksCommand(command, name, state string, data map[string]any) ([]string, error) {
	log := serviceLog(state)
	data["log"] = log
	switch command {
	case "service install":
		return schtasksInstall(name, state, log, data)
	case "service uninstall":
		data["removed"] = false
		if _, ok := queryTask(name); !ok {
			return []string{"The task " + name + " is not installed."}, nil
		}
		_, _ = schtasks("/End", "/TN", name) // fails when it does not run
		if out, err := schtasks("/Delete", "/TN", name, "/F"); err != nil {
			return nil, schtasksFailed("/Delete /TN "+name, out)
		}
		data["removed"] = true
		return []string{"Stopped and removed the task " + name + ".", "The state directory, your data and the log " + log + " stay as they are."}, nil
	}
	return schtasksStatus(name, state, log, data), nil
}

func schtasksInstall(name, state, log string, data map[string]any) ([]string, error) {
	exe, err := serviceBinary()
	if err != nil {
		return nil, err
	}
	account, err := taskUser()
	if err != nil {
		return nil, &stack.Error{Code: "integration_unavailable", Message: "cannot find the current user"}
	}
	content, err := renderTask(account, exe, state)
	if err != nil {
		return nil, err
	}
	old, installed := queryTask(name)
	_, _, healthErr := health(state)
	// Only one service can own a state directory, as with systemd.
	if !installed && healthErr == nil {
		return nil, &stack.Error{Code: "conflict", Message: "a-stack serve already runs for " + state + " outside Task Scheduler; press Ctrl-C in its terminal first, then run a-stack service install again"}
	}
	status := "created"
	if installed {
		status = "updated"
		// Replace old direct actions too: they may have the right paths but
		// still expand a literal %VAR% when the task runs.
		safePaths := !strings.ContainsAny(exe+state, "%") || strings.Contains(old, encodedTaskArguments(exe, "serve --state-dir "+windowsArg(state)))
		if binary, dir, enabled := taskCommand(old); binary == exe && dir == state && enabled && safePaths {
			status = "unchanged"
		}
	}
	if status != "unchanged" {
		f, err := os.CreateTemp("", "a-stack-task-*.xml")
		if err != nil {
			return nil, &stack.Error{Code: "storage_unavailable", Message: "cannot write the task file: " + err.Error()}
		}
		defer os.Remove(f.Name())
		_, err = f.Write(utf16File(content))
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, &stack.Error{Code: "storage_unavailable", Message: "cannot write the task file: " + err.Error()}
		}
		if out, err := schtasks("/Create", "/XML", f.Name(), "/TN", name, "/F"); err != nil {
			return nil, schtasksFailed("/Create /TN "+name, out)
		}
	}
	running := installed && healthErr == nil
	if !running {
		if out, err := schtasks("/Run", "/TN", name); err != nil {
			// A new task that cannot run goes again, so that the failed
			// install leaves nothing behind.
			if status == "created" {
				_, _ = schtasks("/Delete", "/TN", name, "/F")
			}
			return nil, schtasksFailed("/Run /TN "+name, out)
		}
	}
	data["unit_file_status"], data["binary"], data["state_dir"] = status, exe, state
	lines := []string{
		map[string]string{"created": "Created", "updated": "Updated", "unchanged": "Kept"}[status] + " the Task Scheduler task " + name + map[string]string{"created": ".", "updated": "; it differed from the task of this a-stack.", "unchanged": "; it is up to date."}[status],
		"It runs " + exe + " serve --state-dir " + state + ".",
	}
	url, err := waitForService(state)
	if err != nil {
		return nil, &stack.Error{Code: "integration_unavailable", Message: "Task Scheduler started the task " + name + ", but the service does not answer; see schtasks /Query /TN " + name + " /V /FO LIST and " + log, Retryable: true}
	}
	data["url"] = url
	lines = append(lines, "Task Scheduler runs a-stack at "+url+". It starts when you log in, and again within a minute after it stops.")
	if status == "updated" && running {
		hint := "The running service still uses the old task. To use the new one, run: " + stack.ServiceCommand("restart")
		data["restart_hint"] = hint
		lines = append(lines, hint)
	}
	return lines, nil
}

func schtasksStatus(name, state, log string, data map[string]any) []string {
	content, installed := queryTask(name)
	data["installed"] = installed
	column := func(label, text string) string { return fmt.Sprintf("%-11s%s", label, text) }
	lines := []string{column("Task:", name)}
	if !installed {
		lines[0] = column("Task:", "not installed; run a-stack service install")
	} else {
		binary, dir, enabled := taskCommand(content)
		data["binary"], data["state_dir"], data["enabled"] = binary, dir, enabled
		if _, err := os.Stat(binary); err != nil {
			binary += " (missing)"
		}
		lines = append(lines, column("Enabled:", fmt.Sprint(enabled)), column("Binary:", binary), column("State dir:", dir))
		if dir != state {
			lines = append(lines, column("", "The CLI uses "+state+"; run a-stack service install to use it in the task too."))
		}
	}
	lines = append(lines, column("Log:", log))
	return append(lines, healthLine(state, data))
}
