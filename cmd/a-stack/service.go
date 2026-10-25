package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/mzmzeee/a-stack/contrib/systemd"
	"github.com/mzmzeee/a-stack/internal/stack"
)

// Tests replace these, so that they never run the real systemctl or
// launchctl. serviceOS picks the service manager: launchd on macOS (see
// launchd.go), Task Scheduler on Windows (see schtasks.go), systemd
// otherwise.
var (
	runCommand = func(name string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	serviceWait = 10 * time.Second
	serviceOS   = runtime.GOOS
)

// defaultExecStart is the command of the embedded unit. install replaces it
// when the binary or the state directory is somewhere else.
const defaultExecStart = "ExecStart=%h/.local/bin/a-stack serve --state-dir %h/.local/state/a-stack\n"

func isServiceCommand(command string) bool {
	return command == "service install" || command == "service uninstall" || command == "service status"
}

func systemctl(args ...string) (string, error) {
	return runCommand("systemctl", append([]string{"--user"}, args...)...)
}

var plainUnit = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// unitName is a-stack. A_STACK_SERVICE_UNIT changes it only to try service
// install next to a real a-stack.service; it is not documented.
func unitName() (string, error) {
	name := os.Getenv("A_STACK_SERVICE_UNIT")
	if name == "" {
		return "a-stack", nil
	}
	if !plainUnit.MatchString(name) {
		return "", invalid("A_STACK_SERVICE_UNIT must be a plain unit name")
	}
	return name, nil
}

// unitFile is the user unit path, below XDG_CONFIG_HOME or ~/.config.
func unitFile(unit, home string) (string, error) {
	config := os.Getenv("XDG_CONFIG_HOME")
	if config == "" {
		config = filepath.Join(home, ".config")
	} else if !filepath.IsAbs(config) {
		return "", invalid("XDG_CONFIG_HOME must be absolute")
	}
	return filepath.Join(config, "systemd", "user", unit+".service"), nil
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9/._+@-]+$`)

// unitWord writes path as one word of an ExecStart line: %h/rel when it is
// that path in the home folder, else quoted when it has other characters.
// systemd reads % as a specifier and $ as a variable, so they are doubled.
func unitWord(path, home, rel string) (string, error) {
	if home != "" && path == filepath.Join(home, rel) {
		return "%h/" + rel, nil
	}
	if strings.ContainsFunc(path, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", invalid("cannot write a path with control characters into the unit: " + printable(path))
	}
	word := strings.NewReplacer("%", "%%", "$", "$$").Replace(path)
	if plainWord.MatchString(path) {
		return word, nil
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(word) + `"`, nil
}

// unitWords splits an ExecStart command as unitWord writes it.
func unitWords(line, home string) []string {
	var words []string
	var b strings.Builder
	inWord, quoted := false, false
	for i := 0; i < len(line); i++ {
		c := line[i]
		next := byte(0)
		if i+1 < len(line) {
			next = line[i+1]
		}
		switch {
		case c == ' ' && !quoted:
			if inWord {
				words = append(words, b.String())
				b.Reset()
			}
			inWord = false
			continue
		case c == '"':
			quoted = !quoted
		case c == '\\' && next != 0, (c == '%' || c == '$') && next == c:
			b.WriteByte(next)
			i++
		case c == '%' && next == 'h':
			b.WriteString(home)
			i++
		default:
			b.WriteByte(c)
		}
		inWord = true
	}
	if inWord {
		words = append(words, b.String())
	}
	return words
}

// renderUnit is the embedded unit with a command for this binary and state
// directory.
func renderUnit(exe, state, home string) (string, error) {
	binary, err := unitWord(exe, home, ".local/bin/a-stack")
	if err != nil {
		return "", err
	}
	dir, err := unitWord(state, home, ".local/state/a-stack")
	if err != nil {
		return "", err
	}
	return strings.Replace(systemd.Unit, defaultExecStart, "ExecStart="+binary+" serve --state-dir "+dir+"\n", 1), nil
}

// unitCommand returns the binary and the --state-dir of a unit's ExecStart.
func unitCommand(unit, home string) (binary, state string) {
	for _, line := range strings.Split(unit, "\n") {
		if rest, ok := strings.CutPrefix(line, "ExecStart="); ok {
			words := unitWords(rest, home)
			for i, w := range words {
				if i == 0 {
					binary = w
				} else if w == "--state-dir" && i+1 < len(words) {
					state = words[i+1]
				}
			}
			return binary, state
		}
	}
	return "", ""
}

// health asks the service of state, like a-stack health.
func health(state string) (stack.Health, string, error) {
	var h stack.Health
	client, err := stack.NewClient(state, "", stack.OperatorCapability)
	if err != nil {
		return h, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	data, err := client.Do(ctx, "GET", "/v1/health", nil)
	if err != nil {
		return h, "", err
	}
	var envelope struct {
		Data stack.Health `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return h, "", &stack.Error{Code: "storage_unavailable", Message: "invalid service response", Retryable: true}
	}
	return envelope.Data, client.Discovery.URL, nil
}

// linger reports loginctl's Linger setting: yes, no or unknown.
func linger() (value, name string) {
	u, err := user.Current()
	if err != nil {
		return "unknown", ""
	}
	out, err := runCommand("loginctl", "show-user", u.Username, "-p", "Linger")
	if v, ok := strings.CutPrefix(out, "Linger="); err == nil && ok {
		return v, u.Username
	}
	return "unknown", u.Username
}

func lingerHint(name string) string {
	return "To start a-stack at boot, before you log in, run: loginctl enable-linger " + name
}

func systemdFailed(step, out string) error {
	return &stack.Error{Code: "integration_unavailable", Message: "systemctl --user " + step + " failed: " + printable(out), Retryable: true}
}

// serviceCommand runs service install, uninstall or status. It needs no
// credential, and it never touches the state directory.
func serviceCommand(command, state string, machine bool, stdout io.Writer) error {
	unit, err := unitName()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return invalid("cannot find home directory")
	}
	var file string
	switch serviceOS {
	case "darwin":
		unit, file = launchdLabel(unit), launchdFile(unit, home)
	case "windows": // a task has no file of its own
	default:
		if file, err = unitFile(unit, home); err != nil {
			return err
		}
	}
	data := map[string]any{"unit": unit}
	if file != "" {
		data["unit_file"] = file
	}
	var lines []string
	switch {
	case serviceOS == "windows":
		lines, err = schtasksCommand(command, unit, state, data)
	case serviceOS == "darwin":
		lines, err = launchdCommand(command, unit, file, state, home, data)
	case command == "service install":
		lines, err = serviceInstall(unit, file, state, home, data)
	case command == "service uninstall":
		lines, err = serviceUninstall(unit, file, data)
	default:
		lines = serviceStatus(unit, file, state, home, data)
	}
	if err != nil {
		return err
	}
	if machine {
		return json.NewEncoder(stdout).Encode(map[string]any{"data": data})
	}
	_, err = io.WriteString(stdout, strings.Join(lines, "\n")+"\n")
	return err
}

// serviceBinary is the real path of the running a-stack binary.
func serviceBinary() (string, error) {
	exe, err := executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", &stack.Error{Code: "integration_unavailable", Message: "cannot find the running a-stack binary"}
	}
	return exe, nil
}

// installFile writes content to file unless it is there already, and says
// whether it was created, updated or unchanged.
func installFile(file, content string) (string, error) {
	old, err := os.ReadFile(file)
	status := "unchanged"
	if os.IsNotExist(err) {
		status = "created"
	} else if err != nil {
		return "", &stack.Error{Code: "storage_unavailable", Message: "cannot read " + file}
	} else if string(old) != content {
		status = "updated"
	}
	if status != "unchanged" {
		if err = writeFileAtomic(file, content); err != nil {
			return "", &stack.Error{Code: "storage_unavailable", Message: "cannot write " + file + ": " + err.Error()}
		}
	}
	return status, nil
}

// installedLines describe the file and the command of an installed service.
func installedLines(status, kind, file, exe, state string) []string {
	return []string{
		map[string]string{"created": "Wrote ", "updated": "Updated ", "unchanged": "Kept "}[status] + file + map[string]string{"created": ".", "updated": "; it differed from the " + kind + " of this a-stack.", "unchanged": "; it is up to date."}[status],
		"It runs " + exe + " serve --state-dir " + state + ".",
	}
}

// waitForService waits until the service of state answers, and returns its URL.
func waitForService(state string) (url string, err error) {
	for deadline := time.Now().Add(serviceWait); ; time.Sleep(200 * time.Millisecond) {
		if _, url, err = health(state); err == nil || time.Now().After(deadline) {
			return url, err
		}
	}
}

func serviceInstall(unit, file, state, home string, data map[string]any) ([]string, error) {
	exe, err := serviceBinary()
	if err != nil {
		return nil, err
	}
	content, err := renderUnit(exe, state, home)
	if err != nil {
		return nil, err
	}
	// Only one service can own a state directory. A foreground serve would
	// make the unit fail with exit code 3, so it must stop first.
	_, err = systemctl("is-active", "--quiet", unit)
	wasActive := err == nil
	if !wasActive {
		if _, _, err := health(state); err == nil {
			return nil, &stack.Error{Code: "conflict", Message: "a-stack serve already runs for " + state + " outside systemd; press Ctrl-C in its terminal first, then run a-stack service install again"}
		}
	}
	status, err := installFile(file, content)
	if err != nil {
		return nil, err
	}
	// When systemd does not take a new unit, remove it again, so that the
	// failed install leaves nothing behind.
	undo := func(err error) error {
		if status == "created" {
			_, _ = systemctl("disable", unit)
			os.Remove(file)
			_, _ = systemctl("daemon-reload")
		}
		return err
	}
	if out, err := systemctl("daemon-reload"); err != nil {
		return nil, undo(systemdFailed("daemon-reload", out))
	}
	if out, err := systemctl("enable", "--now", unit); err != nil {
		return nil, undo(systemdFailed("enable --now "+unit, out))
	}
	data["unit_file_status"], data["binary"], data["state_dir"] = status, exe, state
	lines := installedLines(status, "unit", file, exe, state)
	if _, err := os.Stat(file + ".d"); err == nil {
		lines = append(lines, "The drop-ins in "+file+".d also apply; check that they do not change ExecStart.")
	}
	url, err := waitForService(state)
	if err != nil {
		return nil, &stack.Error{Code: "integration_unavailable", Message: "systemd started " + unit + ".service, but the service does not answer; see systemctl --user status " + unit + " and journalctl --user -u " + unit, Retryable: true}
	}
	data["url"] = url
	lines = append(lines, "systemd runs a-stack at "+url+". It starts again after a crash and when you log in.")
	if status == "updated" && wasActive {
		hint := "The running service still uses the old unit. To use the new one, run: systemctl --user restart " + unit
		data["restart_hint"] = hint
		lines = append(lines, hint)
	}
	value, name := linger()
	data["linger"] = value
	if value == "no" {
		lines = append(lines, lingerHint(name))
	}
	return lines, nil
}

// writeFileAtomic writes a 0644 file through a temporary file and a rename.
func writeFileAtomic(file, content string) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(file)+"-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(content)
	if err == nil {
		err = tmp.Chmod(0o644)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return stack.Rename(tmp.Name(), file)
}

func serviceUninstall(unit, file string, data map[string]any) ([]string, error) {
	data["removed"] = false
	if _, err := os.Lstat(file); err != nil {
		return []string{unit + ".service is not installed in " + file + "."}, nil
	}
	if out, err := systemctl("disable", "--now", unit); err != nil {
		return nil, systemdFailed("disable --now "+unit, out)
	}
	if err := os.Remove(file); err != nil {
		return nil, &stack.Error{Code: "storage_unavailable", Message: "cannot remove " + file + ": " + err.Error()}
	}
	if out, err := systemctl("daemon-reload"); err != nil {
		return nil, systemdFailed("daemon-reload", out)
	}
	data["removed"] = true
	return []string{"Stopped and disabled " + unit + ".service and removed " + file + ".", "The state directory and your data stay as they are."}, nil
}

// systemdUnitFile is the file that systemd loaded the unit from, or "".
func systemdUnitFile(unit string) string {
	path, err := systemctl("show", "-p", "FragmentPath", "--value", unit)
	if err != nil {
		return ""
	}
	return path
}

// serviceProgram is the binary that the service manager runs as the a-stack
// service, from the unit, plist or task it loaded, and whether it runs now.
func serviceProgram() (binary string, running bool) {
	unit, err := unitName()
	if err != nil {
		return "", false
	}
	switch serviceOS {
	case "darwin":
		return launchdProgram(launchdLabel(unit))
	case "windows":
		content, ok := queryTask(unit)
		binary, _, _ = taskCommand(content)
		return binary, ok
	}
	home, err := os.UserHomeDir()
	content, readErr := os.ReadFile(systemdUnitFile(unit))
	if err != nil || readErr != nil {
		return "", false
	}
	binary, _ = unitCommand(string(content), home)
	_, err = systemctl("is-active", "--quiet", unit)
	return binary, err == nil
}

func serviceStatus(unit, file, state, home string, data map[string]any) []string {
	content, err := os.ReadFile(file)
	installed := err == nil
	// systemd can load the unit from elsewhere, such as from a package in
	// /usr/lib/systemd/user; then Enabled and Active are about that one.
	loaded := ""
	if !installed {
		if path := systemdUnitFile(unit); path != "" && path != file {
			if content, err = os.ReadFile(path); err == nil {
				loaded = path
			}
		}
	}
	enabled, _ := systemctl("is-enabled", unit)
	active, _ := systemctl("is-active", unit)
	value, name := linger()
	data["installed"], data["enabled"], data["active"], data["linger"] = installed, enabled, active, value
	column := func(label, text string) string { return fmt.Sprintf("%-11s%s", label, text) }
	lines := []string{column("Unit file:", file)}
	if loaded != "" {
		data["loaded_unit_file"] = loaded
		lines[0] = column("Unit file:", loaded+" (a-stack service install would write "+file+")")
	} else if !installed {
		lines[0] = column("Unit file:", "not installed; run a-stack service install")
	}
	lines = append(lines, column("Enabled:", enabled), column("Active:", active))
	if installed || loaded != "" {
		binary, dir := unitCommand(string(content), home)
		data["binary"], data["state_dir"] = binary, dir
		if _, err := os.Stat(binary); err != nil {
			binary += " (missing)"
		}
		lines = append(lines, column("Binary:", binary), column("State dir:", dir))
		if dir != state {
			lines = append(lines, column("", "The CLI uses "+state+"; run a-stack service install to use it in the unit too."))
		}
		if _, err := os.Stat(file + ".d"); err == nil {
			data["drop_ins"] = file + ".d"
			lines = append(lines, column("Drop-ins:", file+".d"))
		}
	}
	if value == "no" {
		value += "; " + lingerHint(name)
	}
	lines = append(lines, column("Linger:", value))
	return append(lines, healthLine(state, data))
}

// healthLine is the status line of a-stack health for state.
func healthLine(state string, data map[string]any) string {
	h, url, err := health(state)
	if err != nil {
		e, ok := err.(*stack.Error)
		if !ok {
			e = &stack.Error{Code: "storage_unavailable", Message: err.Error()}
		}
		data["health"] = map[string]any{"error": e}
		return fmt.Sprintf("%-11s%s", "Health:", e.Message)
	}
	data["health"] = map[string]any{"data": h, "url": url}
	return fmt.Sprintf("%-11sa-stack %s answers at %s", "Health:", h.Version, url)
}
