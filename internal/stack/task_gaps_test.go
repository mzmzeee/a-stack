package stack

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotPageRejectsMalformedAndForeignCursors(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	a, b := f.create(t, "Cursor A"), f.create(t, "Cursor B")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: a.ResourceID, AgentID: f.aID, ExpectedRevision: 1})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: a.ResourceID, AgentID: f.bID, ExpectedRevision: 2})

	browser := issueTest(t, s, BrowserCapability, opaqueID(), f.sessions[0])
	page, err := s.SnapshotPage(ctx, browser.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "assignments", TaskID: a.ResourceID, Limit: 1})
	if err != nil || page.Next == "" {
		t.Fatal("missing assignment continuation", err)
	}

	request := SnapshotPageRequest{Context: f.ref(0), Kind: "assignments", TaskID: a.ResourceID, After: page.Next}
	// Deliberately malformed external cursors exercise validation through the
	// public read method; no decoder/encoder helper is used to validate itself.
	encoded := func(v any) string {
		t.Helper()

		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}

		return base64.RawURLEncoding.EncodeToString(body)
	}

	for _, tc := range []struct {
		name   string
		change func(*SnapshotPageRequest)
	}{
		{"too long", func(r *SnapshotPageRequest) { r.After = strings.Repeat("a", 1025) }},
		{"bad base64", func(r *SnapshotPageRequest) { r.After = "%%%" }},
		{"bad JSON", func(r *SnapshotPageRequest) { r.After = base64.RawURLEncoding.EncodeToString([]byte("{")) }},
		{"foreign Session", func(r *SnapshotPageRequest) { r.Context = f.ref(1) }},
		{"foreign Task", func(r *SnapshotPageRequest) { r.TaskID = b.ResourceID }},
		{"foreign collection", func(r *SnapshotPageRequest) { r.Kind = "dependencies" }},
		{"different pinned revision", func(r *SnapshotPageRequest) { r.ReadRevision = page.ReadRevision + 1 }},
		{"negative limit", func(r *SnapshotPageRequest) { r.Limit = -1 }},
		{"excessive limit", func(r *SnapshotPageRequest) { r.Limit = 101 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request
			tc.change(&r)
			// The browser credential cannot read Session 1; the Operator can, so
			// the foreign-Session case fails only on the cursor.
			token := browser.Token
			if tc.name == "foreign Session" {
				token = f.operator.Token
			}

			_, err := s.SnapshotPage(ctx, token, r)
			requireCode(t, err, "invalid_input")
		})
	}

	for _, tc := range []struct{ kind, task, position string }{
		{"history", a.ResourceID, "0"},
		{"history", a.ResourceID, "9223372036854775808"},
		{"graph_history", "", "not-a-sequence"},
		{"assignments", a.ResourceID, a.ResourceID},
		{"assignments", a.ResourceID, b.ResourceID + "/" + f.aID},
		{"assignments", a.ResourceID, a.ResourceID + "/invalid"},
		{"tasks", "", "not-an-id"},
	} {
		t.Run(tc.kind+"/"+tc.position, func(t *testing.T) {
			r := SnapshotPageRequest{Context: f.ref(0), Kind: tc.kind, TaskID: tc.task}
			r.After = encoded(map[string]any{"session_id": r.SessionID, "task_id": r.TaskID, "kind": r.Kind, "read_revision": page.ReadRevision, "position": tc.position})
			_, err := s.SnapshotPage(ctx, browser.Token, r)
			requireCode(t, err, "invalid_input")
		})
	}

	for _, cursor := range []map[string]any{
		{"session_id": f.sessions[0], "task_id": a.ResourceID, "kind": "assignments", "read_revision": 0, "position": a.ResourceID + "/" + f.aID},
		{"session_id": f.sessions[0], "task_id": a.ResourceID, "kind": "assignments", "read_revision": page.ReadRevision, "position": a.ResourceID + "/" + f.aID, "unexpected": true},
	} {
		r := request
		r.After = encoded(cursor)
		_, err := s.SnapshotPage(ctx, browser.Token, r)
		requireCode(t, err, "invalid_input")
	}

	continued, err := s.SnapshotPage(ctx, browser.Token, request)
	if err != nil || len(continued.Items) != 1 || continued.Next != "" || continued.ReadRevision != page.ReadRevision || reflect.DeepEqual(continued.Items, page.Items) {
		t.Fatal("valid composite cursor lost the final assignee", err)
	}

	f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: a.ResourceID, ExpectedRevision: 3, Report: pointer("Advance visible revision")})
	_, err = s.SnapshotPage(ctx, browser.Token, request)
	requireCode(t, err, "conflict")

	if e := err.(*Error); e.CurrentRevision == nil || *e.CurrentRevision != page.ReadRevision+1 {
		t.Fatal("stale cursor did not supply the new read revision")
	}
}

func TestSnapshotAssignmentPaginationCrossesTaskBoundaries(t *testing.T) {
	f := tasksFixture(t)

	s := f.service.Store
	var want []string
	for _, title := range []string{"First shared Task", "Second shared Task"} {
		task := f.create(t, title)
		for i, id := range []string{f.aID, f.bID} {
			f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, AgentID: id, ExpectedRevision: int64(i + 1)})
			want = append(want, task.ResourceID+"/"+id)
		}
	}

	seen := map[string]bool{}
	r := SnapshotPageRequest{Context: f.ref(0), Kind: "assignments", Limit: 1}
	for i := range len(want) {
		page, err := s.SnapshotPage(ctx, f.a.Token, r)
		if err != nil || len(page.Items) != 1 {
			t.Fatal("assignment continuation omitted a shared Task", err)
		}

		a := page.Items[0].(Assignment)

		key := a.TaskID + "/" + a.AgentID
		if seen[key] {
			t.Fatal("composite continuation repeated an assignment")
		}

		seen[key] = true

		if (i == len(want)-1) != (page.Next == "") {
			t.Fatal("assignment continuation ended early or continued past the last page")
		}

		r.After = page.Next
	}

	for _, key := range want {
		if !seen[key] {
			t.Fatal("assignment collection omitted a Task/Agent pair")
		}
	}

	empty, err := s.SnapshotPage(ctx, f.operator.Token, SnapshotPageRequest{Context: f.ref(1), Kind: "assignments", Limit: 100})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 || empty.Next != "" {
		t.Fatal("empty Session did not return a terminal empty collection", err)
	}
}

func TestTaskHTTPReadAliasesEnforceCollectionAndTaskContext(t *testing.T) {
	f := tasksFixture(t)
	task := f.create(t, "Alias target")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, AgentID: f.aID, ExpectedRevision: 1})

	browser := issueTest(t, f.service.Store, BrowserCapability, opaqueID(), f.sessions[0])
	for _, tc := range []struct {
		alias, kind string
		count       int
	}{
		{"list", "tasks", 1}, {"history", "history", 2}, {"assignees", "assignments", 1}, {"references", "references", 0}, {"dependencies", "dependencies", 0},
	} {
		t.Run(tc.alias, func(t *testing.T) {
			r := SnapshotPageRequest{Context: f.ref(0)}
			if tc.alias != "list" {
				r.TaskID = task.ResourceID
			}

			body, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}

			path := "/v1/task/" + tc.alias

			status, data := rawRequest(t, f.service, browser, "POST", path, bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
			var envelope struct{ Data SnapshotPage }
			if status != 200 || json.Unmarshal(data, &envelope) != nil || envelope.Data.Items == nil || len(envelope.Data.Items) != tc.count || envelope.Data.ReadRevision < 1 {
				t.Fatalf("valid read alias failed: HTTP %d %s", status, data)
			}

			if tc.count > 0 {
				item := envelope.Data.Items[0].(map[string]any)
				if tc.kind == "tasks" && item["id"] != task.ResourceID || tc.kind == "history" && item["task_id"] != task.ResourceID || tc.kind == "assignments" && item["agent_id"] != f.aID {
					t.Fatal("alias returned another collection or Task")
				}
			}

			r.Kind = tc.kind

			body, err = json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}

			status, _ = rawRequest(t, f.service, browser, "POST", path, bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
			if status != http.StatusOK {
				t.Fatal("read alias rejected its matching explicit collection")
			}

			for _, bad := range []struct{ method, body string }{
				{"PUT", string(body)},
				{"POST", "{"},
				{"POST", fmt.Sprintf(`{"workspace_id":%q,"session_id":%q,"task_id":%q,"kind":"agents"}`, r.WorkspaceID, r.SessionID, r.TaskID)},
			} {
				status, data := rawRequest(t, f.service, browser, bad.method, path, strings.NewReader(bad.body), map[string]string{"Content-Type": "application/json"})
				var envelope struct{ Error *Error }
				if status != http.StatusBadRequest || json.Unmarshal(data, &envelope) != nil || envelope.Error == nil || envelope.Error.Code != "invalid_input" {
					t.Fatalf("read alias accepted incompatible request: HTTP %d %s", status, data)
				}
			}

			if tc.alias != "list" {
				r.TaskID = ""

				body, err = json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}

				status, _ = rawRequest(t, f.service, browser, "POST", path, bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
				if status != http.StatusBadRequest {
					t.Fatal("detail alias accepted a missing Task ID")
				}
			}
		})
	}
}

func TestDependencyHistoryFailureRollsBackAddAndRemove(t *testing.T) {
	f := tasksFixture(t)

	s := f.service.Store
	a, b := f.create(t, "Atomic prerequisite"), f.create(t, "Atomic dependent")
	for _, action := range []string{"add", "remove"} {
		t.Run(action, func(t *testing.T) {
			before, err := s.InspectSession(ctx, f.operator.Token, f.ref(0))
			if err != nil {
				t.Fatal(err)
			}

			edges := countTest(t, s, "SELECT count(*) FROM dependencies")
			history := countTest(t, s, "SELECT count(*) FROM graph_history")
			receipts := countTest(t, s, "SELECT count(*) FROM operation_receipts")
			r := DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: before.GraphRevision, PrerequisiteID: a.ResourceID, DependentID: b.ResourceID}
			execTest(t, s, `CREATE TRIGGER fail_graph_history BEFORE INSERT ON graph_history BEGIN SELECT RAISE(ABORT,'injected graph history failure'); END`)
			_, err = s.ChangeDependency(ctx, f.orch.Token, action, r)
			requireCode(t, err, "storage_unavailable")

			after, err := s.InspectSession(ctx, f.operator.Token, f.ref(0))
			if err != nil || !reflect.DeepEqual(before, after) || countTest(t, s, "SELECT count(*) FROM dependencies") != edges || countTest(t, s, "SELECT count(*) FROM graph_history") != history || countTest(t, s, "SELECT count(*) FROM operation_receipts") != receipts {
				t.Fatal("failed graph mutation retained partial edge, history, revision or receipt", err)
			}

			execTest(t, s, "DROP TRIGGER fail_graph_history")

			if _, err = s.ChangeDependency(ctx, f.orch.Token, action, r); err != nil {
				t.Fatal("same graph operation did not recover after rollback", err)
			}
		})
	}
}

func TestDependencyRemovalReplayAfterRestartPreservesRecreatedEdge(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store

	a, b := f.create(t, "Retained prerequisite"), f.create(t, "Retained dependent")
	before, err := s.InspectSession(ctx, f.operator.Token, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}

	r := DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: before.GraphRevision, PrerequisiteID: a.ResourceID, DependentID: b.ResourceID}

	added, err := s.ChangeDependency(ctx, f.orch.Token, "add", r)
	if err != nil {
		t.Fatal(err)
	}

	r.OperationID, r.ExpectedGraphRevision = opaqueID(), added.Revision

	removed, err := s.ChangeDependency(ctx, f.orch.Token, "remove", r)
	if err != nil {
		t.Fatal(err)
	}

	recreate := r
	recreate.OperationID, recreate.ExpectedGraphRevision = opaqueID(), removed.Revision

	recreated, err := s.ChangeDependency(ctx, f.orch.Token, "add", recreate)
	if err != nil {
		t.Fatal(err)
	}

	if err = f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	restarted, err := Start(ctx, f.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	s = restarted.Store

	replay, err := s.ChangeDependency(ctx, f.orch.Token, "remove", r)
	if err != nil || replay != removed {
		t.Fatal("restart lost the original removal receipt", err)
	}

	changed := r
	changed.ExpectedGraphRevision = recreated.Revision
	_, err = s.ChangeDependency(ctx, f.orch.Token, "remove", changed)
	requireCode(t, err, "operation_conflict")

	page, err := s.SnapshotPage(ctx, f.operator.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "dependencies"})
	if err != nil || len(page.Items) != 1 || page.Items[0].(Dependency).ID != recreated.ResourceID {
		t.Fatal("old removal retry deleted the recreated edge", err)
	}

	after, err := s.InspectSession(ctx, f.operator.Token, f.ref(0))
	if err != nil || after.GraphRevision != recreated.Revision || countTest(t, s, "SELECT count(*) FROM graph_history") != 3 {
		t.Fatal("replay or conflicting payload changed graph revision/history", err)
	}
}
