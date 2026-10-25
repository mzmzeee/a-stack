package stack

import "testing"

// A new Task stores no row for an empty description or its starting reason,
// and every read shows the same texts as before.
func TestNewTaskStoresNoEmptyTexts(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	empty, err := s.CreateTask(ctx, f.orch.Token, TaskCreateRequest{Context: f.ref(0), OperationID: "empty-texts", Title: "Empty"})
	if err != nil {
		t.Fatal(err)
	}
	described := f.create(t, "Described")
	if n := countTest(t, s, "SELECT count(*) FROM task_content"); n != 1 {
		t.Fatalf("text rows = %d, want only the one description", n)
	}
	for _, c := range []struct {
		id, description string
	}{{empty.ResourceID, ""}, {described.ResourceID, "Work description"}} {
		task := f.show(t, c.id)
		if task.Description != c.description || task.Reason != NotStarted || task.Report != "" || task.Result != "" {
			t.Fatalf("show = %q %q %q %q", task.Description, task.Reason, task.Report, task.Result)
		}
		texts := f.lastHistory(t, c.id).Texts
		if len(texts) != 2 || texts[0].Kind != "description" || texts[0].Body != c.description || texts[1].Kind != "reason" || texts[1].Body != NotStarted || texts[1].ID != "" || texts[1].ActorID != f.orchID {
			t.Fatalf("create history texts = %+v", texts)
		}
	}
	// Setting the same texts again stores nothing, as before.
	noDescription := ""
	f.change(t, f.orch.Token, "edit", TaskChangeRequest{TaskID: empty.ResourceID, ExpectedRevision: 1, Description: &noDescription})
	f.assignA(t, empty.ResourceID)
	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: empty.ResourceID, ExpectedRevision: 3, Progress: "waiting", Reason: NotStarted})
	if n := countTest(t, s, "SELECT count(*) FROM task_content"); n != 1 || f.lastHistory(t, empty.ResourceID).Changes.TextIDs != nil {
		t.Fatalf("unchanged texts stored %d rows", n)
	}
	// Once started, the reason is whatever the assignee set, also when empty.
	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: empty.ResourceID, ExpectedRevision: 4, Progress: "working"})
	if task := f.show(t, empty.ResourceID); task.Reason != "" || countTest(t, s, "SELECT count(*) FROM task_content WHERE kind='reason'") != 1 {
		t.Fatalf("working reason = %q", task.Reason)
	}
}
