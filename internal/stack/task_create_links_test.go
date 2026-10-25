package stack

import (
	"fmt"
	"testing"
)

// A Task created with prerequisites and assignees gets them in the same
// operation: one graph revision, one history entry per assignment, and a
// retry that adds nothing.
func TestTaskCreateLinksAndAssignsInOneOperation(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	a, b := f.create(t, "A"), f.create(t, "B")
	before, err := s.InspectSession(ctx, f.orch.Token, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}
	create := TaskCreateRequest{Context: f.ref(0), OperationID: "linked-create", Title: "C", PrerequisiteIDs: []string{a.ResourceID, b.ResourceID}, AgentIDs: []string{f.aID, f.bID}}
	receipt, err := s.CreateTask(ctx, f.orch.Token, create)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Revision != 3 {
		t.Fatalf("revision %d, want 3 after two assignments", receipt.Revision)
	}
	after, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))
	if after.GraphRevision != before.GraphRevision+1 {
		t.Fatalf("graph revision %d -> %d, want one step", before.GraphRevision, after.GraphRevision)
	}
	task := f.show(t, receipt.ResourceID)
	if task.Revision != 3 || task.AssigneeCount != 2 || task.PrerequisiteCount != 2 || task.Progress != "waiting" {
		t.Fatalf("linked Task %+v", task.Task)
	}
	if n := countTest(t, s, "SELECT count(*) FROM graph_history WHERE action='add' AND revision="+fmt.Sprint(after.GraphRevision)); n != 2 {
		t.Fatalf("%d graph history rows at the create's revision, want 2", n)
	}
	if n := countTest(t, s, "SELECT count(*) FROM task_history WHERE task_id='"+receipt.ResourceID+"' AND action='task.assign'"); n != 2 {
		t.Fatalf("%d assignment history entries, want 2", n)
	}
	// A retry returns the receipt and adds nothing.
	retry, err := s.CreateTask(ctx, f.orch.Token, create)
	if err != nil || retry != receipt || countTest(t, s, "SELECT count(*) FROM tasks") != 3 || countTest(t, s, "SELECT count(*) FROM dependencies") != 2 || countTest(t, s, "SELECT count(*) FROM task_assignees") != 2 {
		t.Fatal("retry of a linked create added rows")
	}
	changed := create
	changed.AgentIDs = []string{f.aID}
	_, err = s.CreateTask(ctx, f.orch.Token, changed)
	requireCode(t, err, "operation_conflict")
	// An assignee works on the receipt's revision.
	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: receipt.ResourceID, ExpectedRevision: receipt.Revision, Progress: "working"})
}

// A bad prerequisite or assignee fails the whole create.
func TestTaskCreateLinksFailAtomically(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	a := f.create(t, "A")
	_, other := f.register(t, 1, "09-other")
	for _, c := range []struct {
		name string
		r    TaskCreateRequest
		code string
	}{
		{"unknown prerequisite", TaskCreateRequest{PrerequisiteIDs: []string{opaqueID()}}, "not_found"},
		{"prerequisite twice", TaskCreateRequest{PrerequisiteIDs: []string{a.ResourceID, a.ResourceID}}, "invalid_input"},
		{"unknown Agent", TaskCreateRequest{PrerequisiteIDs: []string{a.ResourceID}, AgentIDs: []string{opaqueID()}}, "not_found"},
		{"Agent of another Session", TaskCreateRequest{AgentIDs: []string{other.ResourceID}}, "not_found"},
		{"Agent twice", TaskCreateRequest{AgentIDs: []string{f.aID, f.aID}}, "invalid_input"},
		{"too many", TaskCreateRequest{AgentIDs: make([]string, MaxCreateLinks+1)}, "invalid_input"},
	} {
		c.r.Context, c.r.OperationID, c.r.Title = f.ref(0), opaqueID(), "X"
		_, err := s.CreateTask(ctx, f.orch.Token, c.r)
		if e, ok := err.(*Error); !ok || e.Code != c.code {
			t.Fatalf("%s: %v, want %s", c.name, err, c.code)
		}
	}
	if countTest(t, s, "SELECT count(*) FROM tasks") != 1 || countTest(t, s, "SELECT count(*) FROM dependencies") != 0 || countTest(t, s, "SELECT count(*) FROM task_assignees") != 0 {
		t.Fatal("a failed create left rows")
	}
	_, err := s.CreateTask(ctx, f.a.Token, TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID(), Title: "X", AgentIDs: []string{f.aID}})
	requireCode(t, err, "forbidden")
}
