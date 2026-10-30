package stack

import "os/user"

// WindowsTaskName scopes the Task Scheduler task to the current account's SID.
// Task Scheduler names are machine-wide, unlike systemd user units.
func WindowsTaskName() (string, error) {
	u, err := user.Current()
	if err != nil {
		return "", err
	}

	return "a-stack-" + u.Uid, nil
}

// Separate lines work in both cmd.exe and PowerShell. A restart must wait for
// the old process to exit, otherwise Task Scheduler's IgnoreNew skips /Run.
func windowsServiceCommand(verb, name string) string {
	end := "schtasks /End /TN " + name
	run := "schtasks /Run /TN " + name
	switch verb {
	case "start":
		return "schtasks /Change /TN " + name + " /ENABLE\n" + run
	case "stop":
		return "schtasks /Change /TN " + name + " /DISABLE\n" + end
	case "restart":
		return end + "\nAfter the service stops: " + run
	}

	return ""
}
