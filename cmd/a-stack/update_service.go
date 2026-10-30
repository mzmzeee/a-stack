package main

import (
	"context"
	"io"
	"time"
)

// Tests replace these so updates never restart the real service.
var (
	updateService = serviceProgram
	updateHealth  = healthContext
	updateWait    = 15 * time.Second
)

func restartCommand(unit string) string {
	switch serviceOS {
	case "darwin":
		return "launchctl kickstart -k " + launchdDomain() + "/" + launchdLabel(unit)
	case "windows":
		return "schtasks /End /TN " + unit + " && schtasks /Run /TN " + unit
	default:
		return "systemctl --user restart " + unit
	}
}

func restartService(unit string) error {
	var out string
	var err error
	switch serviceOS {
	case "darwin":
		out, err = launchctl("kickstart", "-k", launchdDomain()+"/"+launchdLabel(unit))
	case "windows":
		out, err = schtasks("/End", "/TN", unit)
		if err == nil {
			for deadline := time.Now().Add(updateWait); taskRunning(unit); time.Sleep(200 * time.Millisecond) {
				if time.Now().After(deadline) {
					return updateFailed("Task Scheduler did not stop " + unit + "; restart it manually: " + restartCommand(unit))
				}
			}

			out, err = schtasks("/Run", "/TN", unit)
		}
	default:
		out, err = systemctl("restart", unit)
	}

	if err != nil {
		return updateFailed("the binary was updated, but the service restart failed: " + printable(out) + "; restart it manually: " + restartCommand(unit))
	}

	return nil
}

// restartUpdatedService leaves stopped and hand-started services alone.
// It polls discovery again each time: the restarted service may change port.
func restartUpdatedService(target string, noRestart bool, machine bool, stdout io.Writer, data map[string]any) (string, error) {
	binary, state, running := updateService()

	unit, err := unitName()
	if err != nil {
		return "", err
	}

	hint := "If a-stack serve is running, stop it with Ctrl-C and run a-stack serve again."
	if running {
		hint = "Restart the service: " + restartCommand(unit)
	}

	data["restart_hint"] = hint
	if noRestart || !running {
		return hint, nil
	}

	if !machine {
		if _, err := io.WriteString(stdout, "Restarting the service: "+restartCommand(unit)+"\n"); err != nil {
			return "", err
		}
	}

	if err := restartService(unit); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), updateWait)
	defer cancel()

	last := ""
	for {
		h, _, err := updateHealth(ctx, state)
		if err == nil {
			last = h.Version
			data["service_version"] = last
			if last == target {
				data["restarted"] = true
				data["restart_hint"] = ""
				return "Restarted the service; it runs " + target, nil
			}
		}

		select {
		case <-ctx.Done():
			message := "the binary was updated, but the service did not come back"
			if last != "" {
				message = "the binary was updated, but the service still answers with " + last + "; the service manager runs " + binary + ". Check its ExecStart, plist or task (including a Homebrew Cellar path)"
			}

			return "", updateFailed(message + "; restart it manually: " + restartCommand(unit))
		case <-time.After(200 * time.Millisecond):
		}
	}
}
