package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type taskTeam struct {
	identityFixture
	orch, a, b       Credential
	orchID, aID, bID string
}

func tasksFixture(t *testing.T) taskTeam {
	t.Helper()
	f := identities(t)
	o, or := f.register(t, 0, "01-orchestrator")
	a, ar := f.register(t, 0, "02-worker")
	b, br := f.register(t, 0, "03-worker")
	f.appoint(t, 0, or.ResourceID, 1)
	return taskTeam{f, o, a, b, or.ResourceID, ar.ResourceID, br.ResourceID}
}

func (f taskTeam) create(t *testing.T, title string) Receipt {
	t.Helper()

	r, err := f.service.Store.CreateTask(ctx, f.orch.Token, TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID(), Title: title, Description: "Work description"})
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func (f taskTeam) change(t *testing.T, token, action string, r TaskChangeRequest) Receipt {
	t.Helper()

	r.Context = f.ref(0)
	if r.OperationID == "" {
		r.OperationID = opaqueID()
	}

	out, err := f.service.Store.ChangeTask(ctx, token, action, r)
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func (f taskTeam) show(t *testing.T, id string) TaskDetail {
	t.Helper()

	out, err := f.service.Store.ShowTask(ctx, f.operator.Token, TaskReadRequest{Context: f.ref(0), TaskID: id})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestSharedTaskAuthorityRevisionsAndRevocation(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	create := TaskCreateRequest{Context: f.ref(0), OperationID: "shared-task-create", Title: "Shared", Description: "Complete the whole Task", TicketRef: "#42"}

	task, err := s.CreateTask(ctx, f.orch.Token, create)
	if err != nil {
		t.Fatal(err)
	}

	retry, err := s.CreateTask(ctx, f.orch.Token, create)
	if err != nil || retry != task || countTest(t, s, "SELECT count(*) FROM tasks") != 1 {
		t.Fatal("create retry duplicated Task")
	}

	initial := f.show(t, task.ResourceID)
	if initial.Progress != "waiting" || initial.Reason != "Not started" || !initial.Unassigned {
		t.Fatal("new/unassigned Task model")
	}

	_, err = s.ChangeTask(ctx, f.orch.Token, "status", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 1, Progress: "working"})
	requireCode(t, err, "forbidden")
	_, err = s.ChangeTask(ctx, f.operator.Token, "status", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 1, Progress: "working"})
	requireCode(t, err, "forbidden")
	_, err = s.CreateTask(ctx, f.a.Token, create)
	requireCode(t, err, "forbidden")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 2, AgentID: f.bID})
	type result struct {
		receipt Receipt
		err     error
	}

	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, token := range []string{f.a.Token, f.b.Token} {
		wg.Go(func() {
			r, e := s.ChangeTask(ctx, token, "status", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 3, Progress: "working"})
			results <- result{r, e}
		})
	}

	wg.Wait()
	close(results)

	success, conflict := 0, 0
	for r := range results {
		if r.err == nil {
			success++
		} else {
			requireCode(t, r.err, "conflict")

			e := r.err.(*Error)
			if e.CurrentRevision == nil || *e.CurrentRevision != 4 || e.CurrentProgress != "working" {
				t.Fatal("unhelpful shared revision conflict")
			}

			conflict++
		}
	}

	if success != 1 || conflict != 1 {
		t.Fatal("shared updates overwrote each other")
	}

	for _, token := range []string{f.a.Token, f.b.Token} {
		d, err := s.ShowTask(ctx, token, TaskReadRequest{Context: f.ref(0), TaskID: task.ResourceID})
		if err != nil || d.ID != task.ResourceID || d.Progress != "working" || d.AssigneeCount != 2 {
			t.Fatal("assignees disagree on shared Task")
		}
	}

	priorRequest := TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: "before-removal-status", ExpectedRevision: 4, Progress: "working", Reason: "Contribution continues"}

	prior, err := s.ChangeTask(ctx, f.a.Token, "status", priorRequest)
	if err != nil {
		t.Fatal(err)
	}

	f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: prior.Revision, AgentID: f.aID})
	_, err = s.ChangeTask(ctx, f.a.Token, "status", priorRequest)
	requireCode(t, err, "forbidden")
	_, err = s.ChangeTask(ctx, f.a.Token, "report", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 6, Report: pointer("removed")})
	requireCode(t, err, "forbidden")
	_, err = s.ShowTask(ctx, f.b.Token, TaskReadRequest{Context: f.ref(1), TaskID: task.ResourceID})
	requireCode(t, err, "forbidden")
	_, err = s.CreateTask(ctx, f.orch.Token, TaskCreateRequest{Context: f.ref(1), OperationID: opaqueID(), Title: "Wrong Session"})
	requireCode(t, err, "forbidden")
	duplicate := create
	duplicate.OperationID = opaqueID()
	_, err = s.CreateTask(ctx, f.orch.Token, duplicate)
	requireCode(t, err, "conflict")
	// Role removal is immediate; ordinary assignment remains independent.
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 6, AgentID: f.orchID})
	f.appoint(t, 0, f.bID, 2)
	_, err = s.ChangeTask(ctx, f.orch.Token, "edit", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 7, Title: pointer("Forbidden")})
	requireCode(t, err, "forbidden")
	f.change(t, f.orch.Token, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 7, Progress: "working"})
	f.change(t, f.b.Token, "edit", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 8, Title: pointer("Replacement controls definitions")})
}
func pointer(s string) *string { return &s }
func TestTaskOutcomesAttentionReferencesAndTextRetention(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "Whole Task")
	assigned := f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	_, err := s.ChangeTask(ctx, f.a.Token, "status", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: assigned.Revision, Progress: "finished"})
	requireCode(t, err, "invalid_input")
	_, err = s.ChangeTask(ctx, f.a.Token, "attention.set", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 2, Question: "  "})
	requireCode(t, err, "invalid_input")
	asked := f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 2, Question: "Which release should this target?"})
	before, _ := s.Snapshot(ctx, f.operator.Token, SnapshotRequest{Context: f.ref(0)})
	first := f.show(t, task.ResourceID)

	second := f.show(t, task.ResourceID)
	after, _ := s.Snapshot(ctx, f.operator.Token, SnapshotRequest{Context: f.ref(0)})
	if first.Question == nil || second.Question.ID != first.Question.ID || !first.NeedsYou || after.Session.ReadRevision != before.Session.ReadRevision {
		t.Fatal("view consumed attention or wrote state")
	}

	_, err = s.ChangeTask(ctx, f.a.Token, "attention.set", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 2, Question: "Overwrite?"})
	requireCode(t, err, "conflict")

	if e := err.(*Error); e.CurrentQuestion == nil || e.CurrentQuestion.ID != first.Question.ID || e.CurrentQuestion.Question != first.Question.Question {
		t.Fatal("second question did not return original")
	}

	finished := f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: asked.Revision, Progress: "finished", Summary: "Completed the whole shared outcome"})
	if d := f.show(t, task.ResourceID); d.Result != "Completed the whole shared outcome" || d.Question == nil || !d.NeedsYou {
		t.Fatal("completion cleared attention or lost result")
	}

	reopened := f.change(t, f.a.Token, "reopen", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: finished.Revision, Reason: "Follow-up required"})

	cancelled := f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: reopened.Revision, Progress: "cancelled", Reason: "Approach abandoned"})
	reopened = f.change(t, f.a.Token, "reopen", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: cancelled.Revision, Reason: "New approach"})
	if d := f.show(t, task.ResourceID); d.Progress != "waiting" || d.Result != "Completed the whole shared outcome" || d.Question == nil {
		t.Fatal("reopen discarded outcomes/attention")
	}

	_, err = s.ChangeTask(ctx, f.a.Token, "attention.resolve", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: reopened.Revision})
	requireCode(t, err, "invalid_input")
	resolved := f.change(t, f.a.Token, "attention.resolve", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: reopened.Revision, Note: "Operator chose release v1"})
	report := strings.Repeat("retained report ", 1000)
	r := f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: resolved.Revision, Report: &report})

	r = f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: r.Revision, Report: &report})
	var bodies int
	if s.db.QueryRow("SELECT count(*) FROM task_content WHERE task_id=? AND kind='report'", task.ResourceID).Scan(&bodies) != nil || bodies != 1 {
		t.Fatal("unchanged large report duplicated in storage")
	}

	r = f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: r.Revision, Report: pointer("New report")})

	added := f.change(t, f.a.Token, "reference.add", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: r.Revision, Location: "/not/read/by/server/results.md", ReferenceRevision: "abc123"})
	refs, err := s.SnapshotPage(ctx, f.a.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "references", TaskID: task.ResourceID})
	if err != nil || len(refs.Items) != 1 {
		t.Fatal("reference list", err)
	}

	reference := refs.Items[0].(FileReference)
	f.change(t, f.a.Token, "reference.remove", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: added.Revision, ReferenceID: reference.ID})

	if d := f.show(t, task.ResourceID); d.NeedsYou || d.Question != nil || d.ReferenceCount != 0 || d.Report != "New report" {
		t.Fatal("current detail mismatch")
	}

	history, err := s.SnapshotPage(ctx, f.a.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "history", TaskID: task.ResourceID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}

	foundOldReport, foundResult, foundCancellation, foundResolution, foundRemovedRef := false, false, false, false, false
	for _, item := range history.Items {
		h := item.(TaskHistoryEntry)
		for _, text := range h.Texts {
			if text.Body == report {
				foundOldReport = true
			}

			if text.Kind == "result" && text.Body == "Completed the whole shared outcome" {
				foundResult = true
			}

			if text.Body == "Approach abandoned" {
				foundCancellation = true
			}
		}

		if h.Question != nil && h.Question.Note == "Operator chose release v1" {
			foundResolution = true
		}

		if h.Reference != nil && h.Reference.RemovedAt != "" {
			foundRemovedRef = true
		}
	}

	if !foundOldReport || !foundResult || !foundCancellation || !foundResolution || !foundRemovedRef {
		t.Fatal("retained history lost prior evidence")
	}

	var maxHistory int
	if s.db.QueryRow("SELECT max(length(changes)) FROM task_history").Scan(&maxHistory) != nil || maxHistory > 1024 {
		t.Fatal("large bodies copied into history")
	}
}

func TestDependencyBranchCycleRaceAndNoCascades(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	a, b, c, d := f.create(t, "A"), f.create(t, "B"), f.create(t, "C"), f.create(t, "D")
	edge := func(from, to string) (DependencyRequest, Receipt) {
		session, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))
		req := DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: session.GraphRevision, PrerequisiteID: from, DependentID: to}

		r, err := s.ChangeDependency(ctx, f.orch.Token, "add", req)
		if err != nil {
			t.Fatal(err)
		}

		return req, r
	}
	original, receipt := edge(a.ResourceID, b.ResourceID)
	edge(a.ResourceID, c.ResourceID)
	edge(b.ResourceID, d.ResourceID)
	edge(c.ResourceID, d.ResourceID)

	replay, err := s.ChangeDependency(ctx, f.orch.Token, "add", original)
	if err != nil || replay != receipt || countTest(t, s, "SELECT count(*) FROM dependencies") != 4 || countTest(t, s, "SELECT count(*) FROM graph_history") != 4 {
		t.Fatal("duplicate edge/history on retry")
	}

	session, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))
	req := DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: session.GraphRevision, PrerequisiteID: d.ResourceID, DependentID: a.ResourceID}
	_, err = s.ChangeDependency(ctx, f.orch.Token, "add", req)
	requireCode(t, err, "conflict")
	req.OperationID = opaqueID()
	req.PrerequisiteID = c.ResourceID
	req.DependentID = c.ResourceID
	_, err = s.ChangeDependency(ctx, f.orch.Token, "add", req)
	requireCode(t, err, "invalid_input")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: a.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: b.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: a.ResourceID, ExpectedRevision: 2, Progress: "cancelled", Reason: "Prerequisite abandoned"})

	if shown := f.show(t, b.ResourceID); shown.Progress != "waiting" || shown.CancelledPrerequisiteCount != 1 {
		t.Fatal("cancellation cascaded or prerequisite not surfaced")
	}

	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: b.ResourceID, ExpectedRevision: 2, Progress: "finished", Summary: "Independent whole-Task outcome"})

	if f.show(t, c.ResourceID).Progress != "waiting" {
		t.Fatal("sibling state cascaded")
	}

	after, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))
	if after.GraphRevision != session.GraphRevision {
		t.Fatal("progress edits changed topology revision")
	}

	x, y := f.create(t, "X"), f.create(t, "Y")
	latest, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))
	requests := []DependencyRequest{{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: latest.GraphRevision, PrerequisiteID: x.ResourceID, DependentID: y.ResourceID}, {Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: latest.GraphRevision, PrerequisiteID: y.ResourceID, DependentID: x.ResourceID}}
	type result struct {
		request DependencyRequest
		err     error
	}

	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, r := range requests {
		wg.Go(func() { _, e := s.ChangeDependency(ctx, f.orch.Token, "add", r); results <- result{r, e} })
	}

	wg.Wait()
	close(results)

	won, lost := 0, 0
	for result := range results {
		if result.err == nil {
			won++
		} else {
			requireCode(t, result.err, "conflict")
			lost++
			current, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))
			r := result.request
			r.ExpectedGraphRevision = current.GraphRevision
			_, err = s.ChangeDependency(ctx, f.orch.Token, "add", r)
			requireCode(t, err, "conflict")
		}
	}

	if won != 1 || lost != 1 || countTest(t, s, "SELECT count(*) FROM dependencies") != 5 {
		t.Fatal("concurrent opposite edges introduced cycle")
	}

	current, _ := s.InspectSession(ctx, f.orch.Token, f.ref(0))

	_, err = s.ChangeDependency(ctx, f.orch.Token, "remove", DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: current.GraphRevision, PrerequisiteID: a.ResourceID, DependentID: c.ResourceID})
	if err != nil {
		t.Fatal(err)
	}

	if f.show(t, c.ResourceID).Progress != "waiting" {
		t.Fatal("edge removal changed dependent state")
	}
}

func TestTaskClosureRuntimeLossAndConcurrentRemoval(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "Continue without orchestrator runtime")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})

	_, err := s.BindAgent(ctx, f.orch.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.orchID, ExpectedRevision: 1, Runtime: &RuntimeBinding{InstanceID: "orchestrator-instance"}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.ObserveAgent(ctx, f.orch.Token, AgentObserveRequest{Context: f.ref(0), AgentID: f.orchID, ExpectedRevision: 2, RuntimeID: "orchestrator-instance", State: "unknown"})
	if err != nil {
		t.Fatal(err)
	}

	working := f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 2, Progress: "working"})
	// Assignment removal and worker mutation share Task revision/authority checks.
	start := make(chan struct{})
	workerErr := make(chan error, 1)
	removeErr := make(chan error, 1)
	go func() {
		<-start
		_, e := s.ChangeTask(ctx, f.a.Token, "status", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: working.Revision, Progress: "working", Reason: "Racing update"})
		workerErr <- e
	}()
	go func() {
		<-start
		_, e := s.ChangeTask(ctx, f.orch.Token, "unassign", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: working.Revision, AgentID: f.aID})
		removeErr <- e
	}()
	close(start)

	werr, rerr := <-workerErr, <-removeErr
	if werr == nil {
		requireCode(t, rerr, "conflict")
		latest := f.show(t, task.ResourceID)
		f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: latest.Revision, AgentID: f.aID})
	} else {
		requireCode(t, werr, "forbidden")

		if rerr != nil {
			t.Fatal(rerr)
		}
	}

	latest := f.show(t, task.ResourceID)
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: latest.Revision, AgentID: f.aID})
	latest = f.show(t, task.ResourceID)
	closeRequest := SessionChangeRequest{Context: f.ref(0), OperationID: "task-racing-close", ExpectedRevision: 2}
	statusRequest := TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: "task-racing-update", ExpectedRevision: latest.Revision, Progress: "working", Reason: "Race with closure"}
	begin := make(chan struct{})
	closed := make(chan error, 1)
	updated := make(chan error, 1)
	go func() { <-begin; _, e := s.ChangeSession(ctx, f.operator.Token, "close", closeRequest); closed <- e }()
	go func() { <-begin; _, e := s.ChangeTask(ctx, f.a.Token, "status", statusRequest); updated <- e }()
	close(begin)

	if err = <-closed; err != nil {
		t.Fatal(err)
	}

	err = <-updated
	if err != nil {
		requireCode(t, err, "session_closed")
	} else {
		var taskTime, closeTime string
		if err := s.db.QueryRow("SELECT received_at FROM task_history WHERE task_id=? ORDER BY sequence DESC LIMIT 1", task.ResourceID).Scan(&taskTime); err != nil {
			t.Fatal(err)
		}

		if err := s.db.QueryRow("SELECT received_at FROM lifecycle_history WHERE resource_id=? AND action='session.close' ORDER BY sequence DESC LIMIT 1", f.sessions[0]).Scan(&closeTime); err != nil {
			t.Fatal(err)
		}

		a, err := time.Parse(time.RFC3339Nano, taskTime)
		if err != nil {
			t.Fatal(err)
		}

		b, err := time.Parse(time.RFC3339Nano, closeTime)
		if err != nil {
			t.Fatal(err)
		}

		if a.After(b) {
			t.Fatal("Task mutation committed after closure")
		}
	}

	state := f.show(t, task.ResourceID)
	if state.Progress != "working" {
		t.Fatal("closure changed unfinished progress")
	}

	for _, action := range []string{"edit", "assign", "unassign", "status", "reopen", "report", "attention.set", "attention.resolve", "reference.add", "reference.remove"} {
		token := f.a.Token
		if action == "edit" || action == "assign" || action == "unassign" {
			token = f.orch.Token
		}

		_, err = s.ChangeTask(ctx, token, action, TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: state.Revision})
		requireCode(t, err, "session_closed")
	}

	_, err = s.CreateTask(ctx, f.orch.Token, TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID(), Title: "Closed"})
	requireCode(t, err, "session_closed")

	for _, action := range []string{"add", "remove"} {
		_, err = s.ChangeDependency(ctx, f.orch.Token, action, DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: 1})
		requireCode(t, err, "session_closed")
	}

	_, err = s.ChangeSession(ctx, f.operator.Token, "reopen", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 3})
	if err != nil {
		t.Fatal(err)
	}

	if f.show(t, task.ResourceID).Progress != "working" {
		t.Fatal("reopen changed progress")
	}
}

func TestTaskMutationHistoryFailureRollsBackContent(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "Atomic report")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 2, Report: pointer("Original")})
	before := f.show(t, task.ResourceID)
	contentCount := countTest(t, s, "SELECT count(*) FROM task_content")
	snapshot, _ := s.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)})
	execTest(t, s, "CREATE TRIGGER fail_task_history BEFORE INSERT ON task_history BEGIN SELECT RAISE(ABORT,'injected failure'); END")
	request := TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: "rolled-back-report", ExpectedRevision: 3, Report: pointer("Must roll back")}
	_, err := s.ChangeTask(ctx, f.a.Token, "report", request)
	requireCode(t, err, "storage_unavailable")

	after := f.show(t, task.ResourceID)
	current, _ := s.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)})
	if after.Revision != before.Revision || after.Report != before.Report || countTest(t, s, "SELECT count(*) FROM task_content") != contentCount || current.Session.ReadRevision != snapshot.Session.ReadRevision || countTest(t, s, "SELECT count(*) FROM operation_receipts WHERE operation_id='rolled-back-report'") != 0 {
		t.Fatal("failed mutation retained partial data or success receipt")
	}
}

func TestPopulatedV2UpgradeAndV3FailureRecovery(t *testing.T) {
	for _, failMigration := range []bool{false, true} {
		t.Run(fmt.Sprint(failMigration), func(t *testing.T) {
			dir := privateTestDir(t)
			path, err := ensureDatabaseFile(dir)
			if err != nil {
				t.Fatal(err)
			}

			db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_txlock=immediate")
			if err != nil {
				t.Fatal(err)
			}

			if _, err = db.Exec(migration1 + migration2); err != nil {
				t.Fatal(err)
			}

			if _, err = db.Exec(fmt.Sprintf("PRAGMA application_id=%d;PRAGMA user_version=2", applicationID)); err != nil {
				t.Fatal(err)
			}

			id, w, session, agent := opaqueID(), opaqueID(), opaqueID(), opaqueID()

			operator := Credential{Kind: OperatorCapability, Token: opaqueID()}
			worker := Credential{Kind: AgentCapability, Token: opaqueID()}
			if _, err = db.Exec("INSERT INTO metadata VALUES('database_id',?);INSERT INTO capabilities VALUES(?,'operator','original-operator','');INSERT INTO capabilities VALUES(?,'agent',?,?)", id, tokenHash(operator.Token), tokenHash(worker.Token), agent, session); err != nil {
				t.Fatal(err)
			}

			timestamp := now()
			if _, err = db.Exec("INSERT INTO workspaces VALUES(?,?,?);INSERT INTO sessions(id,workspace_id,name,created_at,updated_at) VALUES(?,?,?,?,?);INSERT INTO agents(id,workspace_id,session_id,name,runtime_id,binding,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?);UPDATE sessions SET orchestrator_id=?,revision=2,read_revision=3 WHERE id=?", w, privateTestDir(t), timestamp, session, w, "v2", timestamp, timestamp, agent, w, session, "retained-agent", "v2-runtime", `{"instance_id":"v2-runtime"}`, timestamp, timestamp, agent, session); err != nil {
				t.Fatal(err)
			}

			if _, err = db.Exec(receiptActorStandIn + expiresStandIn + "CREATE TABLE expired_operations(actor TEXT,operation_id TEXT,outcome TEXT);"); err != nil {
				t.Fatal(err)
			}

			old := &Store{db: db, path: path, id: id}
			op := Operation{ID: "v2-retained-operation", Name: "v2.test", Scope: session, Payload: json.RawMessage(`{}`)}
			allow := func(context.Context, *sql.Tx, Principal, Receipt) error { return nil }

			original, err := old.Mutate(ctx, operator.Token, op, allow, func(context.Context, *sql.Tx, Principal) (Receipt, error) {
				return Receipt{ResourceID: agent, Revision: 1, Outcome: "registered"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}

			if _, err = db.Exec(dropReceiptActor + dropExpires + "DROP TABLE expired_operations;"); err != nil {
				t.Fatal(err)
			}

			if failMigration {
				if _, err = db.Exec("CREATE TABLE tasks(marker TEXT);INSERT INTO tasks VALUES('preserve')"); err != nil {
					t.Fatal(err)
				}
			}

			db.Close()

			if err = writePrivate(filepath.Join(dir, "operator.json"), operator, false); err != nil {
				t.Fatal(err)
			}

			service, err := Start(ctx, dir, "")
			if failMigration {
				requireCode(t, err, "storage_unavailable")
				db, _ = sql.Open("sqlite3", path)

				var version, graphColumns int
				var marker string
				if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 2 || db.QueryRow("SELECT count(*) FROM pragma_table_info('sessions') WHERE name='graph_revision'").Scan(&graphColumns) != nil || graphColumns != 0 || db.QueryRow("SELECT marker FROM tasks").Scan(&marker) != nil || marker != "preserve" {
					t.Fatal("v3 failure damaged populated v2")
				}

				if _, err = db.Exec("DROP TABLE tasks"); err != nil {
					t.Fatal(err)
				}

				db.Close()
				service, err = Start(ctx, dir, "")
			}

			if err != nil {
				t.Fatal(err)
			}

			defer service.Close(ctx)

			if service.Store.id != id || countTest(t, service.Store, "PRAGMA user_version") != schemaVersion {
				t.Fatal("upgrade identity/version")
			}

			ref := Context{w, session}

			retained, err := service.Store.InspectSession(ctx, worker.Token, ref)
			if err != nil || retained.Revision != 2 || retained.ReadRevision != 3 || retained.GraphRevision != 1 || retained.OrchestratorID == nil || *retained.OrchestratorID != agent {
				t.Fatal("v2 registration/role lost", err)
			}

			replay, err := service.Store.Mutate(ctx, operator.Token, op, allow, func(context.Context, *sql.Tx, Principal) (Receipt, error) {
				t.Error("upgrade reapplied receipt")
				return Receipt{}, storageError()
			})
			if err != nil || replay != original {
				t.Fatal("v2 receipt lost")
			}

			_, err = service.Store.CreateTask(ctx, worker.Token, TaskCreateRequest{Context: ref, OperationID: opaqueID(), Title: "Upgraded"})
			if err != nil {
				t.Fatal(err)
			}

			var integrity string
			if service.Store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
				t.Fatal("v3 integrity")
			}
		})
	}
}

func TestAttentionRaceReturnsCurrentQuestion(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "One question")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 2, AgentID: f.bID})
	type result struct{ err error }

	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, token := range []string{f.a.Token, f.b.Token} {
		wg.Go(func() {
			_, err := s.ChangeTask(ctx, token, "attention.set", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 3, Question: fmt.Sprintf("Question %d?", i)})
			results <- result{err}
		})
	}

	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for result := range results {
		if result.err == nil {
			successes++
		} else {
			requireCode(t, result.err, "conflict")

			e := result.err.(*Error)
			if e.CurrentQuestion == nil || e.CurrentQuestion.TaskID != task.ResourceID || e.CurrentRevision == nil || *e.CurrentRevision != 4 {
				t.Fatal("competing question omitted current question/revision")
			}

			conflicts++
		}
	}

	if successes != 1 || conflicts != 1 || countTest(t, s, "SELECT count(*) FROM task_questions WHERE resolved_at IS NULL") != 1 {
		t.Fatal("question race overwrote/duplicated attention")
	}
}

func TestRoleReplacementLinearizesWithTaskDefinitionWrite(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "Before replacement")
	start := make(chan struct{})
	edited := make(chan error, 1)
	replaced := make(chan error, 1)
	go func() {
		<-start
		_, err := s.ChangeTask(ctx, f.orch.Token, "edit", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: 1, Title: pointer("Edited before replacement")})
		edited <- err
	}()
	go func() {
		<-start
		_, err := s.ChangeSession(ctx, f.operator.Token, "appoint", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2, AgentID: f.bID})
		replaced <- err
	}()
	close(start)

	editErr, replaceErr := <-edited, <-replaced
	if replaceErr != nil {
		t.Fatal(replaceErr)
	}

	if editErr != nil {
		requireCode(t, editErr, "forbidden")
	}

	latest := f.show(t, task.ResourceID)
	if (editErr == nil && latest.Revision != 2) || (editErr != nil && latest.Revision != 1) {
		t.Fatal("role race changed Task outside old authority")
	}

	_, err := s.ChangeTask(ctx, f.orch.Token, "edit", TaskChangeRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: latest.Revision, Title: pointer("Old role must fail")})
	requireCode(t, err, "forbidden")
	f.change(t, f.b.Token, "edit", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: latest.Revision, Title: pointer("New role without a runtime")})
}

// The cycle check runs inside every dependency add on the one connection. Its
// plan must search the edges out of each reached Task, with or without table
// statistics; a scan of all Session edges per step made it quadratic.
func TestDependencyCycleCheckPlanSearchesOutgoingEdges(t *testing.T) {
	f := tasksFixture(t)

	db := f.service.Store.db
	plan := func() string {
		t.Helper()

		rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+cycleCheckSQL, "a", "s", "b")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()

		var lines []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}

			lines = append(lines, detail)
		}

		return strings.Join(lines, "\n")
	}
	for _, step := range []string{"", "ANALYZE"} {
		if step != "" {
			if _, err := db.ExecContext(ctx, step); err != nil {
				t.Fatal(err)
			}
		}

		got := plan()
		if !strings.Contains(got, "SEARCH d USING COVERING INDEX dependencies_from (session_id=? AND prerequisite_id=?)") || strings.Contains(got, "dependencies_to") {
			t.Fatalf("cycle check plan (%q) scans edges:\n%s", step, got)
		}
	}
}
