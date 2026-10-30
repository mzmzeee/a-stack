package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestUpdateRestartsManagedServices(t *testing.T) {
	for _, manager := range []string{"linux", "darwin", "windows"} {
		for _, mode := range []string{"running", "stopped", "absent", "no-restart", "current", "check", "json", "old-version", "unreachable", "restart-failed"} {
			t.Run(manager+"/"+mode, func(t *testing.T) {
				release := &fakeRelease{latest: "v0.2.0", archives: map[string][]byte{"v0.2.0": tarGz(t, map[string]string{"a-stack": fakeBinary("v0.2.0")})}}
				updateSetup(t, "v0.1.0", release)
				oldOS, oldRun, oldHealth, oldWait := serviceOS, runCommand, updateHealth, updateWait
				t.Cleanup(func() { serviceOS, runCommand, updateHealth, updateWait = oldOS, oldRun, oldHealth, oldWait })
				serviceOS, updateWait, updateService = manager, 10*time.Millisecond, serviceProgram
				t.Setenv("A_STACK_SERVICE_UNIT", "a-stack-update-test")
				unit := "a-stack-update-test"

				binary, state := "/opt/homebrew/Cellar/a-stack/old/a-stack", "/private/test-state"
				var content string
				switch manager {
				case "darwin":
					content, _ = renderPlist(launchdLabel(unit), binary, state, "/log")
				case "windows":
					content, _ = renderTask("test", binary, state)
				default:
					content, _ = renderUnit(binary, state, "")
				}

				file := filepath.Join(t.TempDir(), "unit")
				if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}

				var calls []string
				restarted := false
				runCommand = func(name string, args ...string) (string, error) {
					call := name + " " + strings.Join(args, " ")
					calls = append(calls, call)

					if mode == "absent" {
						return "not installed", errors.New("missing")
					}

					if strings.Contains(call, " restart ") || strings.Contains(call, "kickstart -k") || strings.Contains(call, "/End") || strings.Contains(call, "/Run") {
						if mode == "restart-failed" {
							return "denied", errors.New("denied")
						}

						restarted = true
						return "", nil
					}

					if strings.Contains(call, "FragmentPath") {
						return file, nil
					}

					if strings.Contains(call, " cat ") {
						return content, nil
					}

					if strings.Contains(call, "is-active") {
						if mode == "stopped" {
							return "", errors.New("inactive")
						}

						return "", nil
					}

					if name == "launchctl" {
						active := "running"
						if mode == "stopped" {
							active = "waiting"
						}

						return "state = " + active + "\npath = " + file, nil
					}

					if name == "powershell.exe" {
						if mode == "stopped" || restarted {
							return "3", nil
						}

						return "4", nil
					}

					if name == "schtasks" {
						return content, nil
					}

					t.Fatalf("unexpected command: %s", call)

					return "", nil
				}
				reads := 0
				updateHealth = func(_ context.Context, gotState string) (stack.Health, string, error) {
					reads++

					if !restarted || gotState != state {
						t.Fatalf("health before restart or wrong state: %q", gotState)
					}

					if mode == "unreachable" {
						return stack.Health{}, "", errors.New("unreachable")
					}

					v := "v0.2.0"
					if mode == "old-version" {
						v = "v0.1.0"
					}

					return stack.Health{Version: v}, "", nil
				}

				var args []string
				if mode == "no-restart" {
					args = append(args, "--no-restart")
				}

				if mode == "current" {
					version = "v0.2.0"
				}

				if mode == "check" {
					args = append(args, "--check")
				}

				if mode == "json" {
					args = append(args, "--json")
				}

				code := 0
				if slices.Contains([]string{"old-version", "unreachable", "restart-failed"}, mode) {
					code = 1
				}

				out, errout := runUpdate(t, code, args...)
				wantRestart := !slices.Contains([]string{"stopped", "absent", "no-restart", "current", "check", "restart-failed"}, mode)
				if restarted != wantRestart {
					t.Fatalf("restarted=%v, calls=%v", restarted, calls)
				}

				if mode == "running" && (!strings.Contains(out, "Restarting the service:") || !strings.Contains(out, "Restarted the service; it runs v0.2.0")) {
					t.Fatal(out)
				}

				if mode == "json" && (!strings.Contains(out, `"restarted":true`) || !strings.Contains(out, `"service_version":"v0.2.0"`) || strings.Contains(out, "Restarting")) {
					t.Fatal(out)
				}

				if mode == "old-version" && (!strings.Contains(errout, binary) || !strings.Contains(errout, "v0.1.0")) {
					t.Fatal(errout)
				}

				if code != 0 && !strings.Contains(strings.ReplaceAll(errout, `\u0026`, "&"), restartCommand(unit)) {
					t.Fatal(errout)
				}

				if wantRestart && mode != "restart-failed" && reads == 0 {
					t.Fatal("never checked new version")
				}
			})
		}
	}
}

func TestUpdateWaitsForNewVersion(t *testing.T) {
	oldService, oldRun, oldHealth := updateService, runCommand, updateHealth
	t.Cleanup(func() { updateService, runCommand, updateHealth = oldService, oldRun, oldHealth })
	updateService = func() (string, string, bool) { return "/binary", "/state", true }
	runCommand = func(string, ...string) (string, error) { return "", nil }
	n := 0
	updateHealth = func(context.Context, string) (stack.Health, string, error) {
		n++
		v := "old"
		if n == 2 {
			v = "new"
		}

		return stack.Health{Version: v}, "", nil
	}

	data := map[string]any{}
	if _, err := restartUpdatedService("new", false, false, io.Discard, data); err != nil || n != 2 || data["restarted"] != true {
		t.Fatalf("%v %v %d", data, err, n)
	}
}

func TestUnitCommandUsesLastExecStart(t *testing.T) {
	binary, state := unitCommand("ExecStart=/old serve --state-dir /old-state\nExecStart=\nExecStart=/new serve --state-dir /new-state\n", "")
	if binary != "/new" || state != "/new-state" {
		t.Fatalf("%q %q", binary, state)
	}
}
