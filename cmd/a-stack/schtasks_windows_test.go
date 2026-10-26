package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEncodedTaskAction(t *testing.T) {
	if output := os.Getenv("ASTACK_TEST_ENCODED_TASK"); output != "" {
		data, _ := json.Marshal(os.Args[1:])
		if err := os.WriteFile(output, data, 0600); err != nil {
			os.Exit(99)
		}
		os.Exit(23)
	}
	root := t.TempDir()
	exe := filepath.Join(root, "O'Brien %PATH% café", "a-stack.exe")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, data, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "état '%TEMP%' & space") + `\`
	want := []string{"-test.run=^TestEncodedTaskAction$", "--", "serve", "--state-dir", state}
	args := strings.Join(want[:4], " ") + " " + windowsArg(state)
	for _, shell := range []string{"powershell", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			command, err := exec.LookPath(shell)
			if err != nil {
				t.Skip(err)
			}
			output := filepath.Join(t.TempDir(), "args.json")
			t.Setenv("ASTACK_TEST_ENCODED_TASK", output)
			out, err := exec.Command(command, strings.Fields(encodedTaskArguments(exe, args))...).CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
				t.Fatalf("wrapper lost child exit status: %v: %s", err, out)
			}
			data, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			if json.Unmarshal(data, &got) != nil || !slices.Equal(got, want) {
				t.Fatalf("wrapper changed arguments: %q; want %q", got, want)
			}
		})
	}
}
