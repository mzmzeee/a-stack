package stack

import (
	"strings"
	"testing"
)

// TestStandingOrdersOperatorWritesAgentsRead: only the Operator sets and
// clears a Workspace's standing orders, under a revision guard and an
// operation ID; the Agents of every Session of the Workspace read them in
// session inspect and workspace orders show, and the view in its snapshot.
func TestStandingOrdersOperatorWritesAgentsRead(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	op := f.operator.Token
	set := func(token, id string, revision int64, body string) (Receipt, error) {
		return s.ChangeOrders(ctx, token, OrdersRequest{OperationID: id, WorkspaceID: f.workspace, ExpectedRevision: revision, Body: body})
	}

	// No orders yet: revision 1, and the reads leave them out.
	o, err := s.ReadOrders(ctx, op, Context{WorkspaceID: f.workspace})
	if err != nil || o.Body != "" || o.Revision != 1 || o.WorkspaceID != f.workspace {
		t.Fatalf("orders before: %+v %v", o, err)
	}
	if session, err := s.InspectSession(ctx, f.a.Token, f.ref(0)); err != nil || session.StandingOrders != nil {
		t.Fatalf("inspect before: %+v %v", session.StandingOrders, err)
	}

	// Refusals change nothing.
	if _, err = set(f.orch.Token, "orders-agent", 1, "x"); err == nil {
		t.Fatal("an Orchestrator set standing orders")
	}
	requireCode(t, err, "forbidden")
	for name, body := range map[string]string{"empty": " \n", "too long": strings.Repeat("x", MaxOrdersBytes+1), "control": "bell\a", "invalid UTF-8": "\xff"} {
		_, err = set(op, "orders-bad-"+strings.ReplaceAll(name, " ", "-"), 1, body)
		requireCode(t, err, "invalid_input")
	}
	_, err = set(op, "orders-stale", 2, "x")
	requireCode(t, err, "conflict")

	before, err := s.InspectSession(ctx, op, f.ref(1))
	if err != nil {
		t.Fatal(err)
	}
	body := "Commit as mzmzeee only.\r\nNever change the GitHub About text.\n\tNo Codex workers."
	r, err := set(op, "orders-set-1", 1, body)
	if err != nil || r.Revision != 2 || r.Outcome != "set" || r.ResourceID != f.workspace {
		t.Fatalf("set: %+v %v", r, err)
	}
	if again, err := set(op, "orders-set-1", 1, body); err != nil || again != r {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if _, err = set(op, "orders-set-1", 1, "other"); err == nil {
		t.Fatal("an operation ID was reused for another body")
	}

	// Every Session of the Workspace sees the change: views reload.
	after, err := s.InspectSession(ctx, f.a.Token, f.ref(0))
	if err != nil || after.StandingOrders == nil || after.StandingOrders.Body != body || after.StandingOrders.Revision != 2 || after.StandingOrders.WorkspaceID != "" {
		t.Fatalf("inspect after: %+v %v", after.StandingOrders, err)
	}
	other, err := s.InspectSession(ctx, op, f.ref(1))
	if err != nil || other.ReadRevision != before.ReadRevision+1 || other.StandingOrders.Body != body {
		t.Fatalf("other Session: read revision %d → %d, %+v", before.ReadRevision, other.ReadRevision, other.StandingOrders)
	}
	if o, err = s.ReadOrders(ctx, f.a.Token, f.ref(0)); err != nil || o.Body != body || o.Revision != 2 {
		t.Fatalf("Agent show: %+v %v", o, err)
	}
	browser := issueTest(t, s, BrowserCapability, "orders-view", f.sessions[0])
	snapshot, err := s.Snapshot(ctx, browser.Token, SnapshotRequest{Context: f.ref(0)})
	if err != nil || snapshot.Session.StandingOrders == nil || snapshot.Session.StandingOrders.Body != body {
		t.Fatalf("snapshot: %+v %v", snapshot.Session.StandingOrders, err)
	}
	_, err = s.ReadOrders(ctx, browser.Token, f.ref(0))
	requireCode(t, err, "forbidden")
	_, err = s.ReadOrders(ctx, f.a.Token, Context{WorkspaceID: f.workspace, SessionID: f.sessions[1]})
	requireCode(t, err, "forbidden")

	// The same text again is no change.
	if r, err = set(op, "orders-set-2", 2, body); err != nil || r.Revision != 2 {
		t.Fatalf("same text: %+v %v", r, err)
	}
	r, err = s.ChangeOrders(ctx, op, OrdersRequest{OperationID: "orders-clear", WorkspaceID: f.workspace, ExpectedRevision: 2, Clear: true})
	if err != nil || r.Revision != 3 || r.Outcome != "cleared" {
		t.Fatalf("clear: %+v %v", r, err)
	}
	if after, err = s.InspectSession(ctx, f.a.Token, f.ref(0)); err != nil || after.StandingOrders != nil {
		t.Fatalf("inspect after clear: %+v %v", after.StandingOrders, err)
	}
	if o, err = s.ReadOrders(ctx, op, Context{WorkspaceID: f.workspace}); err != nil || o.Body != "" || o.Revision != 3 {
		t.Fatalf("show after clear: %+v %v", o, err)
	}
	if n := countTest(t, s, "SELECT count(*) FROM lifecycle_history WHERE resource_id='"+f.workspace+"' AND action LIKE 'workspace.orders.%'"); n != 2 {
		t.Fatalf("%d history entries", n)
	}
}
