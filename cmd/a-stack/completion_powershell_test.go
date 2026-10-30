package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompletionPowerShell(t *testing.T) {
	bin, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell not installed")
	}

	f := newTeam(t)

	dir := t.TempDir()
	cli := filepath.Join(dir, "a-stack.exe")
	if out, err := exec.Command("go", "build", "-o", cli, ".").CombinedOutput(); err != nil {
		t.Fatalf("build completion binary: %v %s", err, out)
	}

	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	path := filepath.Join(dir, "completion.ps1")
	if err := os.WriteFile(path, []byte(helpOutput(t, "completion", "powershell")), 0o600); err != nil {
		t.Fatal(err)
	}

	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	for _, test := range []struct{ line, want string }{
		{"a-stack.exe upd", "update"},
		{"a-stack.exe task status --progress wor", "working"},
		{"a-stack.exe --state-dir " + quote(f.state) + " --credential-file " + quote(f.workerFile) + " task show --task ", f.task + "\tBuild the parser"},
	} {
		script := ". " + quote(path) + "; $line = " + quote(test.line) + "; (TabExpansion2 -inputScript $line -cursorColumn $line.Length).CompletionMatches | ForEach-Object { $_.ListItemText + \"`t\" + $_.ToolTip }"
		if out, err := exec.Command(bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput(); err != nil || !strings.Contains(string(out), test.want) {
			t.Fatalf("PowerShell %q: %v %s", test.line, err, out)
		}
	}
}
