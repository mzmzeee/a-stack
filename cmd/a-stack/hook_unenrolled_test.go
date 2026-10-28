package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// This test also runs unchanged on the parent revision: mere folder
// containment must never grant a runtime the installed Agent's identity.
func TestHookUnenrolledSessionDoesNothing(t *testing.T) {
	f := newTeam(t)
	home := hookHome(t)
	id := hookIdentity{state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	child := filepath.Join(f.project, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, worktree := range []string{f.project, home, filepath.Dir(home)} {
		id.worktree = worktree
		for _, cwd := range []string{f.project, child, home} {
			for _, event := range []string{"SessionStart", "Stop", "PostToolUse", "PermissionRequest"} {
				var out bytes.Buffer
				runHook(id, strings.NewReader(`{"session_id":"unrelated","cwd":`+jsonString(cwd)+`,"hook_event_name":"`+event+`"}`), &out)
				if out.Len() != 0 {
					t.Fatal("unenrolled session received identity-bearing output")
				}
			}
		}
	}
	cache := hookCacheDir(testUserCacheDir(t), f.worker)
	if _, err := os.Stat(cache); !os.IsNotExist(err) {
		t.Fatal("unenrolled session wrote cache state")
	}
}
