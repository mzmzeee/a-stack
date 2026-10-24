package stack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLifecycleListsPageWithoutDuplicatesAndKeepAgentScope(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	worker, first := f.register(t, 0, "list-first")
	_, second := f.register(t, 0, "list-second")
	f.register(t, 1, "list-foreign")
	other, err := s.CreateWorkspace(ctx, f.operator.Token, WorkspaceCreateRequest{OperationID: opaqueID(), Path: privateTestDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 1}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, action string
		credential   Credential
		ref          Context
		want         []string
	}{
		{"Operator Workspaces", "workspace.list", f.operator, Context{}, []string{f.workspace, other.ResourceID}},
		{"Operator Sessions", "session.list", f.operator, Context{WorkspaceID: f.workspace}, f.sessions[:]},
		{"Operator Agents", "agent.list", f.operator, f.ref(0), []string{first.ResourceID, second.ResourceID}},
		{"Agent Workspace", "workspace.list", worker, f.ref(0), []string{f.workspace}},
		{"Agent Session", "session.list", worker, f.ref(0), []string{f.sessions[0]}},
		{"Agent team", "agent.list", worker, f.ref(0), []string{first.ResourceID, second.ResourceID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type listPage struct {
				Items []struct{ ID string }
				Next  string
			}
			read := func(after string) listPage {
				t.Helper()
				page, err := s.ListLifecycle(ctx, tc.credential.Token, tc.action, ListRequest{Context: tc.ref, Limit: 1, After: after})
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(page)
				if err != nil {
					t.Fatal(err)
				}
				var result listPage
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			after := ""
			seen := map[string]bool{}
			for range len(tc.want) {
				page := read(after)
				if len(page.Items) != 1 || page.Items[0].ID <= after || seen[page.Items[0].ID] {
					t.Fatalf("invalid continuation: items=%v after=%q", page.Items, after)
				}
				id := page.Items[0].ID
				seen[id] = true
				if len(seen) < len(tc.want) && page.Next != id || len(seen) == len(tc.want) && page.Next != "" {
					t.Fatalf("incorrect next cursor %q after %d items", page.Next, len(seen))
				}
				after = id
			}
			for _, id := range tc.want {
				if !seen[id] {
					t.Fatal("list omitted an authorized item or included another context")
				}
			}
			// A saved final position must yield an empty, terminal page.
			page := read(after)
			if page.Items == nil || len(page.Items) != 0 || page.Next != "" {
				t.Fatalf("final position did not produce an empty page: %+v", page)
			}
		})
	}
	for _, action := range []string{"workspace.list", "session.list", "agent.list"} {
		_, err = s.ListLifecycle(ctx, worker.Token, action, ListRequest{Context: f.ref(1)})
		requireCode(t, err, "forbidden")
		browser := issueTest(t, s, BrowserCapability, opaqueID(), f.sessions[0])
		_, err = s.ListLifecycle(ctx, browser.Token, action, ListRequest{Context: f.ref(0)})
		requireCode(t, err, "forbidden")
	}
	for _, r := range []ListRequest{
		{Context: f.ref(0), Limit: -1},
		{Context: f.ref(0), After: "malformed-position"},
		{Context: Context{WorkspaceID: opaqueID()}},
	} {
		_, err = s.ListLifecycle(ctx, f.operator.Token, "workspace.list", r)
		code := "invalid_input"
		if r.WorkspaceID != f.workspace {
			code = "not_found"
		}
		requireCode(t, err, code)
	}
}

func TestObserveAgentRejectsWrongAuthorityContextAndBinding(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	worker, agent := f.register(t, 0, "observed-worker")
	peer, _ := f.register(t, 0, "observation-peer")
	_, unattached := f.register(t, 0, "unattached-worker")
	foreign, _ := f.register(t, 1, "foreign-observer")
	browser := issueTest(t, s, BrowserCapability, opaqueID(), f.sessions[0])
	if _, err := s.BindAgent(ctx, worker.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: agent.ResourceID, ExpectedRevision: 1, Runtime: &RuntimeBinding{InstanceID: "old-observed-runtime"}}); err != nil {
		t.Fatal(err)
	}
	replacement := Credential{Kind: AgentCapability, Token: opaqueID()}
	bound, err := s.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: agent.ResourceID, ExpectedRevision: 2, Runtime: &RuntimeBinding{InstanceID: "current-observed-runtime"}, CredentialHash: tokenHash(replacement.Token)})
	if err != nil {
		t.Fatal(err)
	}
	valid := AgentObserveRequest{Context: f.ref(0), AgentID: agent.ResourceID, ExpectedRevision: bound.Revision, RuntimeID: "current-observed-runtime", State: "working"}
	before, err := s.ObserveAgent(ctx, replacement.Token, valid)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx, f.operator.Token, SnapshotRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, token, code string
		change            func(*AgentObserveRequest)
	}{
		{"revoked token", worker.Token, "unauthorized", nil},
		{"peer", peer.Token, "forbidden", nil},
		{"foreign Agent", foreign.Token, "forbidden", nil},
		{"browser", browser.Token, "forbidden", nil},
		{"stale revision", replacement.Token, "conflict", func(r *AgentObserveRequest) { r.ExpectedRevision-- }},
		{"old runtime", replacement.Token, "conflict", func(r *AgentObserveRequest) { r.RuntimeID = "old-observed-runtime" }},
		{"unattached", f.operator.Token, "conflict", func(r *AgentObserveRequest) { r.AgentID = unattached.ResourceID; r.ExpectedRevision = 1 }},
		{"wrong Workspace", replacement.Token, "invalid_input", func(r *AgentObserveRequest) { r.WorkspaceID = opaqueID() }},
		{"wrong Session", replacement.Token, "forbidden", func(r *AgentObserveRequest) { r.Context = f.ref(1) }},
		{"unknown state", replacement.Token, "invalid_input", func(r *AgentObserveRequest) { r.State = "finished" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			r.State = "stopped"
			if tc.change != nil {
				tc.change(&r)
			}
			_, err := s.ObserveAgent(ctx, tc.token, r)
			requireCode(t, err, tc.code)
		})
	}
	after, err := s.ListLifecycle(ctx, f.operator.Token, "agent.list", ListRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range after.(Page[Agent]).Items {
		if a.ID == agent.ResourceID && !reflect.DeepEqual(a, before) {
			t.Fatal("rejected observation changed current runtime metadata")
		}
	}
	current, err := s.Snapshot(ctx, f.operator.Token, SnapshotRequest{Context: f.ref(0)})
	if err != nil || current.Session != snapshot.Session {
		t.Fatal("rejected observation changed Session revisions", err)
	}
}

func TestObserveAgentRollsBackWhenReadRevisionCannotAdvance(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	worker, agent := f.register(t, 0, "atomic-observer")
	bound, err := s.BindAgent(ctx, worker.Token, "attach", AgentBindingRequest{Context: f.ref(0), AgentID: agent.ResourceID, OperationID: opaqueID(), ExpectedRevision: 1, Runtime: &RuntimeBinding{InstanceID: "atomic-runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	r := AgentObserveRequest{Context: f.ref(0), AgentID: agent.ResourceID, ExpectedRevision: bound.Revision, RuntimeID: "atomic-runtime", State: "working"}
	before, err := s.ObserveAgent(ctx, worker.Token, r)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.Snapshot(ctx, worker.Token, SnapshotRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	history, receipts := countTest(t, s, "SELECT count(*) FROM lifecycle_history"), countTest(t, s, "SELECT count(*) FROM operation_receipts")
	execTest(t, s, `CREATE TRIGGER fail_observation_revision BEFORE UPDATE OF read_revision ON sessions BEGIN SELECT RAISE(ABORT,'injected read revision failure'); END`)
	r.State = "stopped"
	_, err = s.ObserveAgent(ctx, worker.Token, r)
	requireCode(t, err, "storage_unavailable")
	page, err := s.ListLifecycle(ctx, worker.Token, "agent.list", ListRequest{Context: f.ref(0)})
	if err != nil || len(page.(Page[Agent]).Items) != 1 || !reflect.DeepEqual(page.(Page[Agent]).Items[0], before) {
		t.Fatal("failed observation retained partial metadata", err)
	}
	current, err := s.Snapshot(ctx, worker.Token, SnapshotRequest{Context: f.ref(0)})
	if err != nil || current.Session != snapshot.Session {
		t.Fatal("failed observation advanced a revision", err)
	}
	execTest(t, s, "DROP TRIGGER fail_observation_revision")
	if _, err = s.ObserveAgent(ctx, f.operator.Token, r); err != nil {
		t.Fatal("observation could not recover after storage failure", err)
	}
	if countTest(t, s, "SELECT count(*) FROM lifecycle_history") != history || countTest(t, s, "SELECT count(*) FROM operation_receipts") != receipts {
		t.Fatal("replaceable observation added domain history or receipts")
	}
}

func TestWorkspaceCanonicalDuplicateAndHistoryFailureAreAtomic(t *testing.T) {
	service, _, operator := startTest(t)
	s := service.Store
	project := privateTestDir(t)
	alias := filepath.Join(privateTestDir(t), "project-alias")
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	r := WorkspaceCreateRequest{OperationID: "atomic-workspace-create", Path: alias}
	worker := issueTest(t, s, AgentCapability, opaqueID(), opaqueID())
	_, err := s.CreateWorkspace(ctx, worker.Token, r)
	requireCode(t, err, "forbidden")
	execTest(t, s, `CREATE TRIGGER fail_workspace_history BEFORE INSERT ON lifecycle_history BEGIN SELECT RAISE(ABORT,'injected history failure'); END`)
	_, err = s.CreateWorkspace(ctx, operator.Token, r)
	requireCode(t, err, "storage_unavailable")
	if countTest(t, s, "SELECT count(*) FROM workspaces") != 0 || countTest(t, s, "SELECT count(*) FROM operation_receipts") != 0 {
		t.Fatal("failed Workspace creation left a registration or receipt")
	}
	execTest(t, s, "DROP TRIGGER fail_workspace_history")
	created, err := s.CreateWorkspace(ctx, operator.Token, r)
	if err != nil {
		t.Fatal("same operation could not recover after rollback", err)
	}
	r.Path = project
	_, err = s.CreateWorkspace(ctx, operator.Token, r)
	requireCode(t, err, "operation_conflict")
	r.OperationID = "duplicate-canonical-workspace"
	_, err = s.CreateWorkspace(ctx, operator.Token, r)
	requireCode(t, err, "conflict")
	listed, err := s.ListLifecycle(ctx, operator.Token, "workspace.list", ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	items := listed.(Page[Workspace]).Items
	if len(items) != 1 || items[0].ID != created.ResourceID || items[0].Path != project || countTest(t, s, "SELECT count(*) FROM lifecycle_history") != 1 || countTest(t, s, "SELECT count(*) FROM operation_receipts") != 1 {
		t.Fatal("canonical aliases duplicated Workspace identity or history")
	}
}
