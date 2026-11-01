package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type protocolMatrixFixture struct {
	team   *team
	hook   hookIdentity
	client *stack.Client
	ctx    context.Context
}

func newProtocolMatrixFixture(t *testing.T) protocolMatrixFixture {
	t.Helper()

	home := filepath.Join(hookHome(t), "private")
	if err := stack.MkdirPrivate(home); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"HOME", "USERPROFILE", "AppData", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, home)
	}

	f := newTeam(t)
	id := hookIdentity{harness: "claude", state: f.state, credential: f.workerFile, agent: f.worker, worktree: f.project, ref: stack.Context{WorkspaceID: f.workspace, SessionID: f.session}}
	enrollTestHook(t, id, "abc", "")

	client, err := stack.NewClient(f.state, f.workerFile, stack.AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	return protocolMatrixFixture{f, id, client, ctx}
}

func (f protocolMatrixFixture) mcp(t *testing.T, name string, args any, want string) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range mcpTools {
		if tool.Name == name {
			data, err := mcpCall(f.ctx, f.client, f.hook.ref, tool, raw)
			if want != "allowed" {
				var failure *stack.Error
				if !errors.As(err, &failure) || failure.Code != want {
					t.Fatalf("%s: want %s, got %v", name, want, err)
				}

				return nil
			}

			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			var envelope struct{ Data json.RawMessage }
			if err = json.Unmarshal(data, &envelope); err != nil || len(envelope.Data) == 0 {
				t.Fatalf("%s: missing successful data envelope", name)
			}

			return envelope.Data
		}
	}

	t.Fatalf("missing MCP tool %s", name)

	return nil
}

// The service admission matrix cannot prove hook silence: Session reads remain
// allowed when closed. Exercise the real single-event adapter and MCP tool
// dispatcher here, without an installer, native harness or open-ended stdio loop.
func TestProtocolStateMatrix(t *testing.T) {
	for _, state := range []string{"open", "closed"} {
		t.Run("session/"+state, func(t *testing.T) {
			f := newProtocolMatrixFixture(t)
			f.team.taskChange(f.team.workerFile, "protocol-working", 2, "status", "--progress", "working")

			if state == "closed" {
				f.team.receipt("", f.team.scoped("session", "close", "--expected-revision", "2", "--operation-id", "protocol-close")...)
			}

			for _, event := range []string{"SessionStart", "Stop"} {
				t.Run("hook/"+event, func(t *testing.T) {
					output := hookEvent(t, f.hook, event, "")
					if state == "closed" {
						if output != "" {
							t.Fatalf("closed hook must be silent, got %q", output)
						}

						return
					}

					if output == "" {
						t.Fatal("open control hook was silent")
					}

					if event == "SessionStart" {
						hookContextText(t, output, event)
					} else if !strings.Contains(output, `"decision":"block"`) {
						t.Fatal("working Task did not trigger the stop guard")
					}
				})
			}

			t.Run("mcp", func(t *testing.T) {
				f.mcp(t, "session_inspect", map[string]any{}, "allowed")

				want := "allowed"
				if state == "closed" {
					want = "session_closed"
				}

				f.mcp(t, "task_status", map[string]any{"task_id": f.team.task, "expected_revision": 3, "operation_id": "protocol-update", "progress": "working"}, want)
			})
		})
	}

	for _, state := range []string{"waiting", "working", "finished", "cancelled", "archived"} {
		t.Run("task/"+state, func(t *testing.T) {
			f := newProtocolMatrixFixture(t)
			revision := int64(2)
			switch state {
			case "working":
				revision = f.team.taskChange(f.team.workerFile, "protocol-work", revision, "status", "--progress", "working")
			case "finished", "archived":
				revision = f.team.taskChange(f.team.workerFile, "protocol-finish", revision, "status", "--progress", "finished", "--summary", "Done")
			case "cancelled":
				revision = f.team.taskChange(f.team.workerFile, "protocol-cancel", revision, "status", "--progress", "cancelled", "--reason", "Not needed")
			}

			if state == "archived" {
				revision = f.team.taskChange("", "protocol-archive", revision, "archive")
			}

			active := state == "waiting" || state == "working"
			t.Run("hook", func(t *testing.T) {
				text := hookContextText(t, hookEvent(t, f.hook, "SessionStart", ""), "SessionStart")
				if strings.Contains(text, "Build the parser") != active {
					t.Fatal("hook active Task projection disagrees with state")
				}
			})
			t.Run("mcp", func(t *testing.T) {
				var task stack.TaskDetail
				if json.Unmarshal(f.mcp(t, "task_show", map[string]any{"task_id": f.team.task}, "allowed"), &task) != nil {
					t.Fatal("invalid Task result")
				}

				progress := state
				if state == "archived" {
					progress = "finished"
				}

				if task.Progress != progress {
					t.Fatalf("want %s, got %s", progress, task.Progress)
				}

				want := "conflict"
				if active {
					want = "allowed"
				}

				f.mcp(t, "task_status", map[string]any{"task_id": f.team.task, "expected_revision": revision, "operation_id": "protocol-progress", "progress": "working"}, want)
			})
		})
	}
}
