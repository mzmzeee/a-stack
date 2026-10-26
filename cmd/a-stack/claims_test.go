package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestTaskClaimsCLI(t *testing.T) {
	f := newTeam(t)
	out, errout, code := f.run(f.workerFile, f.scoped("task", "show", "--task", f.task)...)
	var shown struct {
		Data stack.TaskDetail `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &shown) != nil {
		t.Fatalf("show: %d %s", code, errout)
	}
	args := f.scoped("task", "claim", "--task", f.task, "--path", "src/", "--path", "docs/readme.md", "--expected-revision", fmt.Sprint(shown.Data.Revision), "--operation-id", "cli-claim-paths")
	out, errout, code = f.run(f.workerFile, args...)
	var result struct {
		Data stack.ClaimResult `json:"data"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &result) != nil || result.Data.Outcome != "claim" || len(result.Data.Warnings) != 0 {
		t.Fatalf("claim: %d %s %s", code, out, errout)
	}
	if again, _, code := f.run(f.workerFile, args...); code != 0 || again != out {
		t.Fatal("retry did not return original receipt")
	}
	out, errout, code = f.run(f.workerFile, f.scoped("task", "claims", "--task", f.task)...)
	if code != 0 || !strings.Contains(out, `"claims":["docs/readme.md","src"]`) {
		t.Fatalf("claims: %d %s %s", code, out, errout)
	}
	out, errout, code = f.run(f.workerFile, f.scoped("session", "inspect")...)
	if code != 0 || !strings.Contains(out, `"path":"src"`) {
		t.Fatalf("inspect: %d %s %s", code, out, errout)
	}
	r := f.receipt(f.workerFile, f.scoped("task", "unclaim", "--task", f.task, "--path", "src", "--path", "docs/readme.md", "--expected-revision", fmt.Sprint(result.Data.Revision), "--operation-id", "cli-unclaim-paths")...)
	if r.Outcome != "unclaim" {
		t.Fatal(r)
	}
	if _, errout, code = f.run("", "workspace", "create", "--path", "one", "--path", "two", "--operation-id", "cli-bad-paths"); code != 2 || !strings.Contains(errout, "--path is given more than once") {
		t.Fatalf("duplicate path: %d %s", code, errout)
	}
}
