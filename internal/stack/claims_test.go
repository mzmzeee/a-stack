package stack

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTaskClaimsOverlapLifecycleAndReads(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	first, second := f.create(t, "Backend").ResourceID, f.create(t, "Review").ResourceID
	for _, id := range []string{first, second} {
		f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: 1, AgentID: f.aID})
	}
	claim := func(id string, remove bool, paths ...string) ClaimResult {
		t.Helper()
		r, err := s.ChangeClaims(ctx, f.a.Token, remove, ClaimRequest{Context: f.ref(0), TaskID: id, OperationID: opaqueID(), ExpectedRevision: f.show(t, id).Revision, Paths: paths})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := claim(first, false, "./src/", "docs/readme.md", "src"); len(r.Warnings) != 0 {
		t.Fatal(r)
	}
	if got := f.show(t, first).Claims; !reflect.DeepEqual(got, []string{"docs/readme.md", "src"}) {
		t.Fatal(got)
	}
	if r := claim(second, false, "src-old"); len(r.Warnings) != 0 {
		t.Fatal("prefix without separator overlapped")
	}
	r := claim(second, false, "src/api.go", "docs")
	if len(r.Warnings) != 1 || r.Warnings[0].TaskID != first || r.Warnings[0].Title != "Backend" || !reflect.DeepEqual(r.Warnings[0].Agents, []string{"02-worker"}) {
		t.Fatalf("warning: %+v", r)
	}
	if !reflect.DeepEqual(r.Warnings[0].Paths, []string{"docs/readme.md", "src"}) {
		t.Fatal(r.Warnings)
	}
	show := f.show(t, first)
	if len(show.ClaimWarnings) != 1 || show.ClaimWarnings[0].TaskID != second {
		t.Fatal(show.ClaimWarnings)
	}
	session, err := s.InspectSession(ctx, f.a.Token, f.ref(0))
	if err != nil || len(session.Claims) != 5 {
		t.Fatalf("session claims %+v %v", session.Claims, err)
	}
	view := issueTest(t, s, BrowserCapability, "claims-view", f.sessions[0])
	page, err := s.SnapshotPage(ctx, view.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		task := item.(Task)
		if task.ID == first {
			found = reflect.DeepEqual(task.Claims, show.Claims)
		}
	}
	if !found {
		t.Fatal("view Task page lost claims")
	}
	if read, err := s.ReadClaims(ctx, view.Token, TaskReadRequest{Context: f.ref(0), TaskID: first}); err != nil || len(read.Warnings) != 1 {
		t.Fatalf("view: %+v %v", read, err)
	}
	claim(second, true, "src/api.go", "docs", "not-claimed")
	if len(f.show(t, first).ClaimWarnings) != 0 {
		t.Fatal("unclaim retained warning")
	}
	claim(second, false, ".")
	if len(f.show(t, first).ClaimWarnings) != 1 {
		t.Fatal("root did not overlap")
	}
	for _, progress := range []string{"finished", "cancelled"} {
		f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: second, ExpectedRevision: f.show(t, second).Revision, Progress: progress, Summary: map[bool]string{true: "Done"}[progress == "finished"], Reason: map[bool]string{true: "Stopped"}[progress == "cancelled"]})
		if len(f.show(t, second).Claims) != 0 || len(f.show(t, first).ClaimWarnings) != 0 {
			t.Fatal("terminal Task kept claims")
		}
		f.change(t, f.a.Token, "reopen", TaskChangeRequest{TaskID: second, ExpectedRevision: f.show(t, second).Revision, Reason: "More work"})
		if len(f.show(t, second).Claims) != 0 {
			t.Fatal("reopen resurrected claims")
		}
		claim(second, false, "src")
	}
}

func TestTaskClaimsRetriesValidationAndRollback(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	id := f.create(t, "Task").ResourceID
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: 1, AgentID: f.aID})
	r := ClaimRequest{Context: f.ref(0), TaskID: id, OperationID: "claim-original", ExpectedRevision: 2, Paths: []string{"src"}}
	got, err := s.ChangeClaims(ctx, f.a.Token, false, r)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.ChangeClaims(ctx, f.a.Token, false, r)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("replay %+v %v", again, err)
	}
	r.Paths = []string{"other"}
	_, err = s.ChangeClaims(ctx, f.a.Token, false, r)
	requireCode(t, err, "operation_conflict")
	r.OperationID = "claim-stale"
	_, err = s.ChangeClaims(ctx, f.a.Token, false, r)
	requireCode(t, err, "conflict")
	r.ExpectedRevision = got.Revision
	for _, token := range []string{f.b.Token, f.operator.Token, issueTest(t, s, BrowserCapability, "claims-browser", f.sessions[0]).Token} {
		r.OperationID = opaqueID()
		_, err = s.ChangeClaims(ctx, token, false, r)
		requireCode(t, err, "forbidden")
	}
	for _, paths := range [][]string{nil, {"../out"}, {"/etc"}, {`C:\src`}, {`\\server\share`}, {"src/*"}, {"x\x00"}, {strings.Repeat("x", 1025)}, make([]string, 101)} {
		r.OperationID = opaqueID()
		r.Paths = paths
		_, err = s.ChangeClaims(ctx, f.a.Token, false, r)
		requireCode(t, err, "invalid_input")
	}
	before := f.show(t, id)
	execTest(t, s, "CREATE TRIGGER fail_claim_receipt BEFORE INSERT ON operation_receipts BEGIN SELECT RAISE(ABORT,'injected'); END")
	r.OperationID = "claim-rollback"
	r.Paths = []string{"rolled-back"}
	_, err = s.ChangeClaims(ctx, f.a.Token, false, r)
	requireCode(t, err, "storage_unavailable")
	after := f.show(t, id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed receipt retained claims or revisions")
	}
	execTest(t, s, "DROP TRIGGER fail_claim_receipt")
	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: id, ExpectedRevision: got.Revision, AgentID: f.aID})
	r.OperationID = "claim-original"
	r.ExpectedRevision = 2
	r.Paths = []string{"src"}
	_, err = s.ChangeClaims(ctx, f.a.Token, false, r)
	requireCode(t, err, "forbidden")
}

func TestTaskClaimsCrossSessionAndWorkspace(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	id := f.create(t, "First Session").ResourceID
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: 1, AgentID: f.aID})
	if _, err := s.ChangeClaims(ctx, f.a.Token, false, ClaimRequest{Context: f.ref(0), TaskID: id, OperationID: opaqueID(), ExpectedRevision: 2, Paths: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	other, agent := f.register(t, 1, "other-orch")
	f.appoint(t, 1, agent.ResourceID, 1)
	second, err := s.CreateTask(ctx, other.Token, TaskCreateRequest{Context: f.ref(1), OperationID: opaqueID(), Title: "Other Session", AgentIDs: []string{agent.ResourceID}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.InspectSession(ctx, f.a.Token, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.ChangeClaims(ctx, other.Token, false, ClaimRequest{Context: f.ref(1), TaskID: second.ResourceID, OperationID: opaqueID(), ExpectedRevision: second.Revision, Paths: []string{"shared/x"}})
	if err != nil || len(result.Warnings) != 1 || result.Warnings[0].TaskID != id {
		t.Fatalf("cross Session: %+v %v", result, err)
	}
	after, err := s.InspectSession(ctx, f.a.Token, f.ref(0))
	if err != nil || after.ReadRevision <= before.ReadRevision {
		t.Fatal("other view not invalidated")
	}
	// Another Workspace inside this same Store must not produce a warning.
	workspace, err := s.CreateWorkspace(ctx, f.operator.Token, WorkspaceCreateRequest{OperationID: opaqueID(), Path: privateTestDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeClaims(ctx, other.Token, true, ClaimRequest{Context: f.ref(1), TaskID: second.ResourceID, OperationID: opaqueID(), ExpectedRevision: result.Revision, Paths: []string{"shared/x"}}); err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateSession(ctx, f.operator.Token, SessionCreateRequest{OperationID: opaqueID(), WorkspaceID: workspace.ResourceID, Name: "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	ref := Context{WorkspaceID: workspace.ResourceID, SessionID: created.ResourceID}
	token := opaqueID()
	registered, err := s.RegisterAgent(ctx, f.operator.Token, AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: "foreign-orch", CredentialHash: tokenHash(token)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeSession(ctx, f.operator.Token, "appoint", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: 1, AgentID: registered.ResourceID}); err != nil {
		t.Fatal(err)
	}
	foreign, err := s.CreateTask(ctx, token, TaskCreateRequest{Context: ref, OperationID: opaqueID(), Title: "Foreign Task", AgentIDs: []string{registered.ResourceID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ChangeClaims(ctx, token, false, ClaimRequest{Context: ref, TaskID: foreign.ResourceID, OperationID: opaqueID(), ExpectedRevision: foreign.Revision, Paths: []string{"shared"}}); err != nil {
		t.Fatal(err)
	}
	if len(f.show(t, id).ClaimWarnings) != 0 {
		t.Fatal("other Workspace overlapped")
	}
	// Read data contains only paths and metadata, never file contents.
	b, _ := json.Marshal(f.show(t, id))
	if !strings.Contains(string(b), `"claims":["shared"]`) {
		t.Fatal(string(b))
	}
}
