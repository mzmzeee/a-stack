package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestSessionExportGolden(t *testing.T) {
	f := newTeam(t)
	f.receipt("", "workspace", "orders", "set", "--workspace", f.workspace, "--body", "Commit as the Operator.\nDo not push.", "--expected-revision", "1", "--operation-id", "export-orders")
	design := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "Z Design", "--assign", f.orch, "--operation-id", "export-design")...).ResourceID
	f.receipt(f.orchFile, f.scoped("task", "report", "--task", design, "--report", "Use the standard library.\nKeep the parser small.", "--expected-revision", "2", "--operation-id", "export-design-report")...)
	f.receipt(f.orchFile, f.scoped("task", "status", "--task", design, "--progress", "finished", "--summary", "Grammar agreed.", "--expected-revision", "3", "--operation-id", "export-design-finish")...)
	f.receipt(f.orchFile, f.scoped("dependency", "add", "--from", design, "--to", f.task, "--expected-revision", "3", "--operation-id", "export-dependency")...)
	f.taskChange(f.workerFile, "export-report", 2, "report", "--report", "Parser half done.\nNext: errors.\x1b")
	f.taskChange(f.workerFile, "export-working", 3, "status", "--progress", "working")
	f.taskChange(f.workerFile, "export-ask", 4, "attention", "set", "--question", "Strict or lenient?")

	out, _, _ := f.run("", f.scoped("task", "show", "--task", f.task)...)
	var detail struct{ Data stack.TaskDetail }
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatal(err)
	}

	f.receipt("", f.scoped("answer", "--task", f.task, "--question-id", detail.Data.Question.ID, "--text", "Lenient,\nwith warnings.", "--operation-id", "export-answer")...)
	f.taskChange(f.workerFile, "export-ask-again", 6, "attention", "set", "--question", "Which errors first?")
	f.receipt(f.orchFile, f.scoped("task", "assign", "--task", f.task, "--agent", f.orch, "--expected-revision", "7", "--operation-id", "export-second-assignee")...)

	for _, task := range []string{f.task, design} {
		credential := f.workerFile
		if task == design {
			credential = f.orchFile
		}

		f.receipt(credential, f.scoped("mail", "send", "--task", task, "--body", "PRIVATE MAIL MUST NOT APPEAR", "--operation-id", "export-mail-"+task)...)
	}

	archived := f.receipt(f.orchFile, f.scoped("task", "create", "--title", "A Archived", "--assign", f.orch, "--operation-id", "export-archived")...).ResourceID
	f.receipt(f.orchFile, f.scoped("task", "status", "--task", archived, "--progress", "finished", "--summary", "Old work.", "--expected-revision", "2", "--operation-id", "export-archive-finish")...)
	f.receipt("", f.scoped("task", "archive", "--task", archived, "--expected-revision", "3", "--operation-id", "export-archive")...)
	f.receipt(f.orchFile, f.scoped("task", "create", "--title", "C Unassigned", "--operation-id", "export-unassigned")...)
	before, _, _ := f.run("", f.scoped("session", "inspect")...)

	golden, err := os.ReadFile("testdata/session-export.md")
	if err != nil {
		t.Fatal(err)
	}

	for _, credential := range []string{"", f.workerFile, f.orchFile} {
		out, errout, code := f.run(credential, f.scoped("session", "export", "--markdown")...)
		if code != 0 || errout != "" || strings.ReplaceAll(out, f.project, "<workspace>") != string(golden) {
			t.Fatalf("export golden: code %d stderr %s\n%s", code, errout, out)
		}
	}

	out, errout, code := f.run(f.workerFile, "session", "export", "--json", "--include-archived") // context from credential
	var exported struct{ Data sessionExport }
	if code != 0 || errout != "" || json.Unmarshal([]byte(out), &exported) != nil || len(exported.Data.Tasks) != 4 {
		t.Fatalf("export JSON: %d %s %s", code, errout, out)
	}

	e := exported.Data
	if e.Name != "Build" || e.WorkspacePath != f.project || e.SessionID != f.session || e.Orders == nil || e.Tasks[0].ID != archived || !e.Tasks[0].Archived || e.Tasks[1].ID != design || e.Tasks[2].ID != f.task || e.Tasks[2].Question == nil || e.Tasks[2].AnsweredQuestion.Answer != "Lenient,\nwith warnings." {
		t.Fatalf("export data: %+v", e)
	}

	if strings.Contains(out, "PRIVATE MAIL") || strings.Contains(out, "messages\"") {
		t.Fatalf("export contains mail bodies: %s", out)
	}

	path := filepath.Join(t.TempDir(), "summary.json")
	if err = os.WriteFile(path, []byte("previous export"), 0o600); err != nil {
		t.Fatal(err)
	}

	fileOut, errout, code := f.run("", f.scoped("session", "export", "--json", "--include-archived", "--output", path)...)
	data, err := os.ReadFile(path)
	if code != 0 || errout != "" || fileOut != "" || err != nil || string(data) != out {
		t.Fatalf("export file: %d %s %s %v", code, fileOut, errout, err)
	}

	out, errout, code = f.run("", f.scoped("session", "export", "--include-archived")...)
	if code != 0 || errout != "" || !strings.Contains(out, "### A Archived\n\nProgress: finished (archived)") {
		t.Fatalf("archived Markdown: %d %s %s", code, errout, out)
	}

	path = filepath.Join(t.TempDir(), "summary.md")
	if _, errout, code = f.run("", f.scoped("session", "export", "--include-archived", "--output", path)...); code != 0 {
		t.Fatalf("Markdown file: %d %s", code, errout)
	}

	if data, err = os.ReadFile(path); err != nil || string(data) != out {
		t.Fatalf("Markdown file differs: %v", err)
	}

	after, _, _ := f.run("", f.scoped("session", "inspect")...)
	if before != after {
		t.Fatal("export changed the Session")
	}

	// Closed Sessions remain readable too.
	f.receipt("", f.scoped("session", "close", "--expected-revision", "2", "--operation-id", "export-close")...)

	if _, errout, code = f.run(f.workerFile, "session", "export"); code != 0 {
		t.Fatalf("closed export: %d %s", code, errout)
	}
}

func TestSessionExportPages(t *testing.T) {
	f := newTeam(t)
	previous := f.task
	for i := range 102 {
		previous = f.receipt(f.orchFile, f.scoped("task", "create", "--title", fmt.Sprintf("Step %03d", 102-i), "--after", previous, "--assign", f.worker, "--operation-id", fmt.Sprintf("export-page-%d", i))...).ResourceID
	}

	out, errout, code := f.run(f.workerFile, "session", "export", "--json")
	var e struct{ Data sessionExport }
	if code != 0 || json.Unmarshal([]byte(out), &e) != nil || len(e.Data.Tasks) != 103 {
		t.Fatalf("paged export: %d %s %s", code, errout, out)
	}

	for i, task := range e.Data.Tasks {
		if len(task.Assignees) != 1 || task.Assignees[0] != "02-worker" || i == 0 && task.ID != f.task || i > 0 && task.Title != fmt.Sprintf("Step %03d", 103-i) {
			t.Fatalf("paged task %d: %+v", i, task)
		}
	}
}

func TestSessionExportEmptyAndFlags(t *testing.T) {
	f := newTeam(t)
	session := f.receipt("", "session", "create", "--workspace", f.workspace, "--name", "Empty", "--operation-id", "export-empty").ResourceID

	out, errout, code := f.run("", "session", "export", "--workspace", f.workspace, "--session", session)
	want := "# Session: Empty\n\nWorkspace folder: " + f.project + "\n\n## Tasks\n\nNo Tasks.\n\n## Needs you\n\nNo open or answered questions.\n"
	if code != 0 || errout != "" || out != want {
		t.Fatalf("empty export: %d %s %q", code, errout, out)
	}

	for _, args := range [][]string{
		{"session", "export", "--markdown", "--json"},
		{"session", "export", "--output="},
		{"session", "export", "--limit", "1"},
		{"session", "export"}, // Operator needs explicit context
	} {
		out, errout, code = f.run("", args...)
		if code != 2 || out != "" || !json.Valid([]byte(errout)) {
			t.Fatalf("%v: %d %s %s", args, code, out, errout)
		}
	}

	for _, path := range []string{filepath.Join(t.TempDir(), "missing", "export.md"), t.TempDir()} {
		out, errout, code = f.run(f.workerFile, "session", "export", "--output", path)
		if code != 2 || out != "" || !strings.Contains(errout, "--output") {
			t.Fatalf("bad output path: %d %s %s", code, errout, out)
		}
	}

	if help := helpOutput(t, "session", "export", "--help"); !strings.Contains(help, "--markdown") || !strings.Contains(help, "--include-archived") || !strings.Contains(help, "--output FILE") {
		t.Fatal(help)
	}
}

func TestSessionExportConflictIsNotPartial(t *testing.T) {
	for _, failAt := range []string{"tasks", "dependencies", "assignments", "detail"} {
		t.Run(failAt, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var result any
				switch r.URL.Path {
				case "/v1/session/snapshot":
					result = stack.SessionSnapshot{Session: stack.Session{Name: "Build", ReadRevision: 7}}
				case "/v1/workspace/list":
					result = map[string]any{"items": []stack.Workspace{{ID: "workspace", Path: "/project"}}}
				case "/v1/session/page", "/v1/task/show":
					var request struct {
						Kind         string `json:"kind"`
						ReadRevision int64  `json:"read_revision"`
					}
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}

					if request.ReadRevision != 7 {
						t.Errorf("un-pinned read: %+v", request)
					}

					if request.Kind == failAt || r.URL.Path == "/v1/task/show" {
						w.WriteHeader(http.StatusConflict)
						fmt.Fprintln(w, `{"error":{"code":"conflict","message":"Session read revision changed; restart the snapshot collection"}}`)
						return
					}

					items := []any{}
					if request.Kind == "tasks" {
						items = append(items, stack.Task{ID: "task", Title: "Task"})
					}

					result = map[string]any{"read_revision": 7, "items": items}
				default:
					t.Errorf("unexpected endpoint (mail must never be read): %s", r.URL.Path)
				}

				_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
			}))
			defer server.Close()
			client := &stack.Client{Discovery: stack.Discovery{URL: server.URL, InstanceID: "test"}, Credential: stack.Credential{Kind: stack.AgentCapability, Token: strings.Repeat("A", 43)}, HTTP: server.Client()}

			path := filepath.Join(t.TempDir(), "summary.md")
			if err := os.WriteFile(path, []byte("previous export"), 0o600); err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			err := exportSession(context.Background(), client, lifecycleFlags{workspace: "workspace", session: "session"}, exportFlags{output: path}, false, &out)
			if e, ok := err.(*stack.Error); !ok || e.Code != "conflict" || out.Len() != 0 {
				t.Fatalf("conflict: %v %s", err, out.String())
			}

			data, err := os.ReadFile(path)
			if err != nil || string(data) != "previous export" {
				t.Fatalf("failed export replaced output: %v %s", err, data)
			}
		})
	}
}

func TestDependencyOrder(t *testing.T) {
	tasks := []stack.Task{{ID: "c", Title: "A"}, {ID: "b", Title: "B", Archived: true}, {ID: "a", Title: "C"}, {ID: "d", Title: "B"}}
	edges := []stack.Dependency{{PrerequisiteID: "a", DependentID: "b"}, {PrerequisiteID: "b", DependentID: "c"}, {PrerequisiteID: "a", DependentID: "d"}, {PrerequisiteID: "d", DependentID: "c"}}

	got, err := dependencyOrder(tasks, edges)
	if err != nil || len(got) != 4 || got[0].ID != "a" || got[1].ID != "b" || got[2].ID != "d" || got[3].ID != "c" {
		t.Fatalf("diamond: %+v %v", got, err)
	}

	slices.Reverse(tasks)
	slices.Reverse(edges)

	again, err := dependencyOrder(tasks, edges)
	if err != nil || !slices.EqualFunc(got, again, func(a, b stack.Task) bool { return a.ID == b.ID }) {
		t.Fatalf("unstable order: %+v %v", again, err)
	}

	for _, bad := range [][]stack.Dependency{{{PrerequisiteID: "missing", DependentID: "a"}}, {{PrerequisiteID: "a", DependentID: "b"}, {PrerequisiteID: "b", DependentID: "a"}}} {
		if _, err = dependencyOrder(tasks, bad); err == nil {
			t.Fatal("accepted incomplete or cyclic graph")
		}
	}
}
