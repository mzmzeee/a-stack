package stack

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clientForCredential(t *testing.T, f taskTeam, c Credential) *Client {
	t.Helper()
	path := filepath.Join(privateTestDir(t), "credential.json")
	if err := writePrivate(path, c, false); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(f.dir, path, c.Kind)
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func TestSnapshotBrowserScopePrivacyAndRevisionPins(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	a, b := f.create(t, "Visible A"), f.create(t, "Visible B")
	private := &RuntimeBinding{InstanceID: "secret-runtime-identity", Endpoint: "secret-endpoint", PaneID: "secret-pane", TerminalID: "secret-terminal", NativeSessionID: "secret-native", HerdrAlias: "secret-herdr-alias"}
	_, err := s.BindAgent(ctx, f.orch.Token, "attach", AgentBindingRequest{Context: f.ref(0), AgentID: f.orchID, ExpectedRevision: 1, OperationID: opaqueID(), Runtime: private})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ObserveAgent(ctx, f.orch.Token, AgentObserveRequest{Context: f.ref(0), AgentID: f.orchID, ExpectedRevision: 2, RuntimeID: private.InstanceID, State: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	browser := issueTest(t, s, BrowserCapability, "view", f.sessions[0])
	client := clientForCredential(t, f, browser)
	data, err := client.Do(ctx, "POST", "/v1/session/snapshot", SnapshotRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Data SessionSnapshot `json:"data"`
	}
	if json.Unmarshal(data, &manifest) != nil || manifest.Data.Counts.Tasks != 2 || manifest.Data.Source != "local-service" {
		t.Fatal("snapshot manifest")
	}
	if _, err = time.Parse(time.RFC3339Nano, manifest.Data.CapturedAt); err != nil {
		t.Fatal("snapshot source/age timestamp")
	}
	var workspacePath string
	if err = s.db.QueryRowContext(ctx, `SELECT path FROM workspaces WHERE id=?`, f.workspace).Scan(&workspacePath); err != nil {
		t.Fatal(err)
	}
	if manifest.Data.WorkspaceName == "" || manifest.Data.WorkspaceName != filepath.Base(workspacePath) || bytes.Contains(data, []byte(filepath.Dir(workspacePath))) {
		t.Fatal("snapshot must name the Workspace folder without its full path")
	}
	unchanged, err := s.Snapshot(ctx, browser.Token, SnapshotRequest{Context: f.ref(0), KnownRevision: manifest.Data.Session.ReadRevision})
	if err != nil || !unchanged.Unchanged || unchanged.Counts != nil || unchanged.WorkspaceName != manifest.Data.WorkspaceName {
		t.Fatal("conditional snapshot")
	}
	for _, kind := range []string{"tasks", "agents", "assignments", "dependencies"} {
		response, err := client.Do(ctx, "POST", "/v1/session/page", SnapshotPageRequest{Context: f.ref(0), Kind: kind, ReadRevision: manifest.Data.Session.ReadRevision})
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{private.InstanceID, private.Endpoint, private.PaneID, private.TerminalID, private.NativeSessionID, private.HerdrAlias, browser.Token, f.operator.Token, f.a.Token} {
			if bytes.Contains(response, []byte(secret)) {
				t.Fatal("snapshot leaked private integration/credential data")
			}
		}
	}
	_, err = client.Do(ctx, "POST", "/v1/session/snapshot", SnapshotRequest{Context: f.ref(1)})
	requireCode(t, err, "forbidden")
	_, err = client.Do(ctx, "POST", "/v1/task/status", TaskChangeRequest{Context: f.ref(0), TaskID: a.ResourceID, ExpectedRevision: 1, OperationID: opaqueID(), Progress: "working"})
	requireCode(t, err, "forbidden")
	page, err := s.SnapshotPage(ctx, browser.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks", Limit: 1})
	if err != nil || page.Next == "" {
		t.Fatal("continuation missing")
	}
	_, err = s.SnapshotPage(ctx, browser.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "agents", After: page.Next})
	requireCode(t, err, "invalid_input")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: b.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	_, err = s.SnapshotPage(ctx, browser.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks", After: page.Next})
	requireCode(t, err, "conflict")
	oldGraph := manifest.Data.Session.GraphRevision
	f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: b.ResourceID, ExpectedRevision: 2, Report: pointer("Report-only update")})
	current, err := s.Snapshot(ctx, browser.Token, SnapshotRequest{Context: f.ref(0), KnownRevision: manifest.Data.Session.ReadRevision})
	if err != nil || current.Unchanged || current.Session.GraphRevision != oldGraph || current.Session.ReadRevision == manifest.Data.Session.ReadRevision {
		t.Fatal("report-only revision shape")
	}
	_, err = client.Do(ctx, "POST", "/v1/session/page", map[string]any{"workspace_id": f.workspace, "session_id": f.sessions[0], "kind": "mail"})
	requireCode(t, err, "invalid_input")
}
func TestTaskReadBudgetAndCompleteRetainedHistory(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	description := strings.Repeat("<", MaxTaskTextBytes)
	created, err := s.CreateTask(ctx, f.orch.Token, TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID(), Title: strings.Repeat("<", 256), Description: description, TicketRef: strings.Repeat("<", 1024)})
	if err != nil {
		t.Fatal(err)
	}
	assigned := f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	revision := assigned.Revision
	reports := []string{strings.Repeat("<", MaxTaskTextBytes), strings.Repeat(">", MaxTaskTextBytes), strings.Repeat("&", MaxTaskTextBytes)}
	for i, report := range reports {
		r := f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: revision, Report: &report})
		revision = r.Revision
		if i == 0 {
			r = f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: revision, Report: &report})
			revision = r.Revision
		}
	}
	asked := f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: revision, Question: strings.Repeat("<", MaxTaskNoteBytes)})
	finished := f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: asked.Revision, Progress: "finished", Summary: strings.Repeat("<", MaxTaskNoteBytes)})
	f.change(t, f.a.Token, "reopen", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: finished.Revision, Reason: strings.Repeat("<", MaxTaskNoteBytes)})
	client := clientForCredential(t, f, f.a)
	data, err := client.Do(ctx, "POST", "/v1/task/show", TaskReadRequest{Context: f.ref(0), TaskID: created.ResourceID})
	if err != nil || len(data) > MaxRequestBytes {
		t.Fatal("detail exceeded client byte limit", err)
	}
	detailBytes := len(data)
	var detail struct {
		Data TaskDetail `json:"data"`
	}
	if json.Unmarshal(data, &detail) != nil || detail.Data.Description != description || detail.Data.Report != reports[2] || detail.Data.Question == nil {
		t.Fatal("detail truncated retained text")
	}
	after := ""
	sequences := map[int64]bool{}
	texts := map[string]bool{}
	pages, maxBytes := 0, 0
	for {
		data, err = client.Do(ctx, "POST", "/v1/task/history", SnapshotPageRequest{Context: f.ref(0), TaskID: created.ResourceID, After: after, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > MaxRequestBytes {
			t.Fatal("history response unbounded")
		}
		if len(data) > maxBytes {
			maxBytes = len(data)
		}
		var envelope struct {
			Data struct {
				ReadRevision int64              `json:"read_revision"`
				Items        []TaskHistoryEntry `json:"items"`
				Next         string             `json:"next"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			t.Fatal("history JSON")
		}
		for _, entry := range envelope.Data.Items {
			if sequences[entry.Sequence] {
				t.Fatal("history replayed/skipped at byte boundary")
			}
			sequences[entry.Sequence] = true
			for _, text := range entry.Texts {
				texts[text.Body] = true
			}
		}
		pages++
		after = envelope.Data.Next
		if after == "" {
			break
		}
		if pages > 20 {
			t.Fatal("history pagination failed to terminate")
		}
	}
	var count int
	if s.db.QueryRow("SELECT count(*) FROM task_history WHERE task_id=?", created.ResourceID).Scan(&count) != nil || len(sequences) != count || pages < 2 {
		t.Fatal("bounded history omitted retained events")
	}
	for _, report := range reports {
		if !texts[report] {
			t.Fatal("prior report missing from retained history")
		}
	}
	if countTest(t, s, "SELECT count(*) FROM task_content WHERE kind='report'") != 3 {
		t.Fatal("same report copied on unchanged update")
	}
	t.Logf("worst escaped detail=%d bytes; history=%d pages, max=%d bytes, %d retained events", detailBytes, pages, maxBytes, len(sequences))
}
func TestSnapshotCollectionsAt10_100_1000Tasks(t *testing.T) {
	for _, n := range []int{10, 100, 1000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := tasksFixture(t)
			s := f.service.Store
			for i := 0; i < n; i++ {
				f.create(t, fmt.Sprintf("Task %04d", i))
			}
			client := clientForCredential(t, f, f.a)
			manifest, err := s.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)})
			if err != nil || manifest.Counts.Tasks != n {
				t.Fatal("manifest lost retained Tasks")
			}
			start := time.Now()
			after := ""
			seen := map[string]bool{}
			pages, totalBytes, maxBytes := 0, 0, 0
			for {
				data, err := client.Do(ctx, "POST", "/v1/task/list", SnapshotPageRequest{Context: f.ref(0), ReadRevision: manifest.Session.ReadRevision, After: after, Limit: 100})
				if err != nil {
					t.Fatal(err)
				}
				var envelope struct {
					Data struct {
						ReadRevision int64  `json:"read_revision"`
						Items        []Task `json:"items"`
						Next         string `json:"next"`
					} `json:"data"`
				}
				if json.Unmarshal(data, &envelope) != nil || envelope.Data.ReadRevision != manifest.Session.ReadRevision {
					t.Fatal("inconsistent read collection")
				}
				for _, task := range envelope.Data.Items {
					if seen[task.ID] {
						t.Fatal("duplicate Task")
					}
					seen[task.ID] = true
					if !task.Unassigned || task.Progress != "waiting" {
						t.Fatal("unassigned Tasks omitted/mislabelled")
					}
				}
				pages++
				totalBytes += len(data)
				if len(data) > maxBytes {
					maxBytes = len(data)
				}
				after = envelope.Data.Next
				if after == "" {
					break
				}
				if pages > n {
					t.Fatal("pagination loop")
				}
			}
			if len(seen) != n || maxBytes > MaxRequestBytes {
				t.Fatal("collection silently omitted Tasks or exceeded client limit")
			}
			t.Logf("%d Tasks: %d pages, %d total bytes, %d maximum page bytes, collection %.3f ms", n, pages, totalBytes, maxBytes, float64(time.Since(start).Microseconds())/1000)
		})
	}
}
func TestTaskDependencyCountsAndReadQueryPlans(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "Indexed counts")
	rows, err := s.db.Query("EXPLAIN QUERY PLAN SELECT "+taskColumns+" FROM tasks t WHERE t.id=? AND t.session_id=?", task.ResourceID, f.sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	searches := 0
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if rows.Scan(&id, &parent, &unused, &detail) != nil {
			t.Fatal("query plan")
		}
		if strings.HasPrefix(detail, "SCAN d") {
			t.Fatal("dependency counts scan retained edges")
		}
		if strings.HasPrefix(detail, "SEARCH d USING COVERING INDEX dependencies_to") {
			searches++
		}
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	rows.Close()
	if searches != 2 {
		t.Fatal("both dependency counts must use Session/endpoint index")
	}
	plans := []struct {
		name, query string
		args        []any
	}{
		{"tasks", "SELECT id FROM tasks WHERE session_id=? AND id>? ORDER BY id LIMIT ?", []any{f.sessions[0], "", 101}},
		{"agents", "SELECT id FROM agents WHERE session_id=? AND id>? ORDER BY id LIMIT ?", []any{f.sessions[0], "", 101}},
		{"dependencies", "SELECT id FROM dependencies WHERE session_id=? AND id>? ORDER BY id LIMIT ?", []any{f.sessions[0], "", 101}},
		{"assignments", "SELECT task_id,agent_id FROM task_assignees WHERE session_id=? AND (task_id,agent_id)>(?,?) ORDER BY task_id,agent_id LIMIT ?", []any{f.sessions[0], "", "", 101}},
		{"history", "SELECT sequence FROM task_history WHERE task_id=? AND sequence>? ORDER BY sequence LIMIT ?", []any{task.ResourceID, 0, 101}},
	}
	for _, plan := range plans {
		rows, err = s.db.Query("EXPLAIN QUERY PLAN "+plan.query, plan.args...)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if rows.Scan(&id, &parent, &unused, &detail) != nil {
				t.Fatal("query plan")
			}
			t.Logf("%s: %s", plan.name, detail)
			if strings.HasPrefix(detail, "SCAN ") {
				t.Fatal("read page scans the full retained table")
			}
		}
		rows.Close()
	}
}
func TestCrossSessionTaskReferencesAndDependenciesFail(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "Domestic")
	other, agent := f.identityFixture.register(t, 1, "other-orchestrator")
	f.identityFixture.appoint(t, 1, agent.ResourceID, 1)
	foreign, err := s.CreateTask(ctx, other.Token, TaskCreateRequest{Context: f.ref(1), OperationID: opaqueID(), Title: "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	current, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))
	_, err = s.ChangeDependency(ctx, f.orch.Token, "add", DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: current.GraphRevision, PrerequisiteID: foreign.ResourceID, DependentID: task.ResourceID})
	requireCode(t, err, "not_found")
	_, err = s.ChangeTask(ctx, f.orch.Token, "assign", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 1, AgentID: agent.ResourceID})
	requireCode(t, err, "not_found")
	if countTest(t, s, "SELECT count(*) FROM dependencies") != 0 || f.show(t, task.ResourceID).AssigneeCount != 0 {
		t.Fatal("cross-Session reference persisted")
	}
}
