package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// On macOS, a-stack service keeps the service running with a launchd agent:
// a plist in ~/Library/LaunchAgents that launchctl loads into the user's GUI
// domain. launchd starts it at login and again after a failure. Unlike
// systemd, it cannot skip exit codes 3 and 4, so it tries again every 10
// seconds until the cause is gone.

// launchdLabel is the label of the agent for unit, dev.a-stack by default
// (stack.LaunchdLabel).
func launchdLabel(unit string) string { return "dev." + unit }

func launchdFile(unit, home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel(unit)+".plist")
}

// launchdLog is the file that gets the service's output, such as its ready line.
func launchdLog(label, home string) string {
	return filepath.Join(home, "Library", "Logs", strings.TrimPrefix(label, "dev.")+".log")
}

func launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func launchctl(args ...string) (string, error) { return runCommand("launchctl", args...) }

// launchdState is the state that launchctl print shows for a loaded agent,
// such as running, or "" when the agent is not loaded.
func launchdState(label string) string {
	out, err := launchctl("print", launchdDomain()+"/"+label)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if state, ok := strings.CutPrefix(strings.TrimSpace(line), "state = "); ok {
			return state
		}
	}
	return "loaded"
}

func launchdFailed(step, out string) error {
	return &stack.Error{Code: "integration_unavailable", Message: "launchctl " + step + " failed: " + printable(out), Retryable: true}
}

const plistHead = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`

// renderPlist is the launchd agent that runs exe serve --state-dir state.
func renderPlist(label, exe, state, log string) (string, error) {
	values := []string{label, exe, state, log}
	for i, v := range values {
		if strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return "", invalid("cannot write a path with control characters into the plist: " + printable(v))
		}
		var b strings.Builder
		_ = xml.EscapeText(&b, []byte(v))
		values[i] = b.String()
	}
	return plistHead + fmt.Sprintf(`	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
		<string>--state-dir</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>Umask</key>
	<integer>63</integer>
	<key>StandardOutPath</key>
	<string>%[4]s</string>
	<key>StandardErrorPath</key>
	<string>%[4]s</string>
</dict>
</plist>
`, values[0], values[1], values[2], values[3]), nil
}

// plistCommand returns the binary and the --state-dir of a plist's
// ProgramArguments.
func plistCommand(content string) (binary, state string) {
	var plist struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Text    string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if xml.Unmarshal([]byte(content), &plist) != nil {
		return "", ""
	}
	items := plist.Dict.Items
	for i, item := range items {
		if item.XMLName.Local == "key" && item.Text == "ProgramArguments" && i+1 < len(items) {
			args := items[i+1].Strings
			for j, a := range args {
				if j == 0 {
					binary = a
				} else if a == "--state-dir" && j+1 < len(args) {
					state = args[j+1]
				}
			}
		}
	}
	return binary, state
}

// launchdCommand runs service install, uninstall or status with launchd.
func launchdCommand(command, label, file, state, home string, data map[string]any) ([]string, error) {
	target, log := launchdDomain()+"/"+label, launchdLog(label, home)
	data["log"] = log
	switch command {
	case "service install":
		return launchdInstall(label, target, file, state, log, data)
	case "service uninstall":
		data["removed"] = false
		if _, err := os.Lstat(file); err != nil {
			return []string{label + " is not installed in " + file + "."}, nil
		}
		if launchdState(label) != "" {
			if out, err := launchctl("bootout", target); err != nil {
				return nil, launchdFailed("bootout "+target, out)
			}
		}
		if err := os.Remove(file); err != nil {
			return nil, &stack.Error{Code: "storage_unavailable", Message: "cannot remove " + file + ": " + err.Error()}
		}
		data["removed"] = true
		return []string{"Stopped " + label + ", unloaded it from launchd and removed " + file + ".", "The state directory, your data and the log " + log + " stay as they are."}, nil
	}
	return launchdStatus(label, file, state, log, data), nil
}

func launchdInstall(label, target, file, state, log string, data map[string]any) ([]string, error) {
	exe, err := serviceBinary()
	if err != nil {
		return nil, err
	}
	content, err := renderPlist(label, exe, state, log)
	if err != nil {
		return nil, err
	}
	// Only one service can own a state directory, as with systemd.
	loaded := launchdState(label) != ""
	if !loaded {
		if _, _, err := health(state); err == nil {
			return nil, &stack.Error{Code: "conflict", Message: "a-stack serve already runs for " + state + " outside launchd; press Ctrl-C in its terminal first, then run a-stack service install again"}
		}
	}
	status, err := installFile(file, content)
	if err != nil {
		return nil, err
	}
	if loaded {
		// Start it if it stopped; a running agent stays as it is.
		if out, err := launchctl("kickstart", target); err != nil {
			return nil, launchdFailed("kickstart "+target, out)
		}
	} else {
		// enable undoes an earlier launchctl disable, which blocks bootstrap.
		if out, err := launchctl("enable", target); err != nil {
			return nil, launchdFailed("enable "+target, out)
		}
		if out, err := launchctl("bootstrap", launchdDomain(), file); err != nil {
			return nil, launchdFailed("bootstrap "+launchdDomain()+" "+file, out)
		}
	}
	data["unit_file_status"], data["binary"], data["state_dir"] = status, exe, state
	lines := installedLines(status, "plist", file, exe, state)
	url, err := waitForService(state)
	if err != nil {
		return nil, &stack.Error{Code: "integration_unavailable", Message: "launchd started " + label + ", but the service does not answer; see launchctl print " + target + " and " + log, Retryable: true}
	}
	data["url"] = url
	lines = append(lines, "launchd runs a-stack at "+url+". It starts again after a failure and when you log in.")
	if status == "updated" && loaded {
		hint := "The running service still uses the old plist. To use the new one, run: launchctl bootout " + target + " && launchctl bootstrap " + launchdDomain() + " " + file
		data["restart_hint"] = hint
		lines = append(lines, hint)
	}
	return lines, nil
}

func launchdStatus(label, file, state, log string, data map[string]any) []string {
	content, err := os.ReadFile(file)
	installed := err == nil
	active := launchdState(label)
	data["installed"], data["loaded"] = installed, active != ""
	if active == "" {
		active = "not loaded"
	}
	data["active"] = active
	column := func(label, text string) string { return fmt.Sprintf("%-11s%s", label, text) }
	lines := []string{column("Plist:", file)}
	if !installed {
		lines[0] = column("Plist:", "not installed; run a-stack service install")
	}
	lines = append(lines, column("Active:", active))
	if installed {
		binary, dir := plistCommand(string(content))
		data["binary"], data["state_dir"] = binary, dir
		if _, err := os.Stat(binary); err != nil {
			binary += " (missing)"
		}
		lines = append(lines, column("Binary:", binary), column("State dir:", dir))
		if dir != state {
			lines = append(lines, column("", "The CLI uses "+state+"; run a-stack service install to use it in the plist too."))
		}
	}
	lines = append(lines, column("Log:", log))
	return append(lines, healthLine(state, data))
}
