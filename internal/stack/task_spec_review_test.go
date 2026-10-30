package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

type taskReviewFixture struct {
	service *Service
	op      Credential
	ref     Context
	agents  [3]Receipt
	creds   [3]Credential
}

func taskReviewSetup(t *testing.T) taskReviewFixture {
	t.Helper()
	service, op := reviewService(t, privateTestDir(t))

	f := taskReviewFixture{service: service, op: op}
	f.ref = lifecycleReviewSession(t, service.Store, op, "")
	for i, name := range []string{"review-orchestrator", "review-worker-a", "review-worker-b"} {
		f.agents[i], f.creds[i] = lifecycleReviewAgent(t, service.Store, op, f.ref, name)
	}

	_, err := service.Store.ChangeSession(context.Background(), op.Token, "appoint", SessionChangeRequest{Context: f.ref, OperationID: opaqueID(), ExpectedRevision: 1, AgentID: f.agents[0].ResourceID})
	if err != nil {
		t.Fatal(err)
	}

	return f
}

func (f taskReviewFixture) create(t *testing.T, title string) Receipt {
	t.Helper()

	r, err := f.service.Store.CreateTask(context.Background(), f.creds[0].Token, TaskCreateRequest{Context: f.ref, OperationID: opaqueID(), Title: title})
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func (f taskReviewFixture) change(t *testing.T, actor int, action string, r TaskChangeRequest) Receipt {
	t.Helper()

	r.Context = f.ref
	if r.OperationID == "" {
		r.OperationID = opaqueID()
	}

	result, err := f.service.Store.ChangeTask(context.Background(), f.creds[actor].Token, action, r)
	if err != nil {
		t.Fatal(err)
	}

	return result
}

func (f taskReviewFixture) show(t *testing.T, id string) TaskDetail {
	t.Helper()

	r, err := f.service.Store.ShowTask(context.Background(), f.creds[1].Token, TaskReadRequest{Context: f.ref, TaskID: id})
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func (f taskReviewFixture) snapshot(t *testing.T) SessionSnapshot {
	t.Helper()

	r, err := f.service.Store.Snapshot(context.Background(), f.op.Token, SnapshotRequest{Context: f.ref})
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func TestTaskSpecSharedAuthorityProgressAndAttention(t *testing.T) {
	ctx := context.Background()
	f := taskReviewSetup(t)
	s := f.service.Store

	task := f.create(t, "one shared Task")
	initial := f.show(t, task.ResourceID)
	if !initial.Unassigned || initial.Progress != "waiting" || initial.Reason != "Not started" {
		t.Fatal("incorrect initial shared state")
	}

	for _, c := range []Credential{f.op, f.creds[0], f.creds[1]} {
		_, err := s.ChangeTask(ctx, c.Token, "status", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: 1, Progress: "working"})
		reviewCode(t, err, "forbidden")
	}

	task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[1].ResourceID})
	task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[2].ResourceID})
	_, err := s.ChangeTask(ctx, f.creds[1].Token, "status", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "finished", Summary: " \n"})
	reviewCode(t, err, "invalid_input")
	results := make(chan struct {
		r   Receipt
		err error
	}, 2)

	start := make(chan struct{})
	competingTask := task
	for i := 1; i <= 2; i++ {
		go func(actor int) {
			<-start
			r, e := s.ChangeTask(ctx, f.creds[actor].Token, "status", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: competingTask.ResourceID, ExpectedRevision: competingTask.Revision, Progress: "working", Reason: "shared contribution"})
			results <- struct {
				r   Receipt
				err error
			}{r, e}
		}(i)
	}

	close(start)

	winners := 0
	for range 2 {
		r := <-results
		if r.err == nil {
			task = r.r
			winners++
			continue
		}

		var e *Error
		if !errors.As(r.err, &e) || e.Code != "conflict" || e.CurrentRevision == nil || e.CurrentProgress != "working" {
			t.Fatal("stale shared write lacked useful conflict")
		}
	}

	if winners != 1 {
		t.Fatal("shared revision had more than one winner")
	}

	question := "Which input should the team use?"
	task = f.change(t, 1, "attention.set", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Question: question})

	_, err = s.ChangeTask(ctx, f.creds[2].Token, "attention.set", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Question: "A replacement question"})
	var conflict *Error
	if !errors.As(err, &conflict) || conflict.Code != "conflict" || conflict.CurrentQuestion == nil || conflict.CurrentQuestion.Question != question {
		t.Fatal("second question overwrote or hid current question")
	}

	task = f.change(t, 2, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "finished", Summary: "whole Task completed"})
	if detail := f.show(t, task.ResourceID); !detail.NeedsYou || detail.Progress != "finished" || detail.Result == "" {
		t.Fatal("completion lost attention or result")
	}

	task = f.change(t, 1, "reopen", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Reason: "new evidence"})
	_, err = s.ChangeTask(ctx, f.creds[2].Token, "status", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "cancelled"})
	reviewCode(t, err, "invalid_input")

	task = f.change(t, 2, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "cancelled", Reason: "no longer needed"})
	if detail := f.show(t, task.ResourceID); !detail.NeedsYou || detail.Result != "whole Task completed" {
		t.Fatal("cancellation erased prior result or question")
	}

	task = f.change(t, 1, "reopen", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Reason: "resume work"})
	task = f.change(t, 2, "attention.resolve", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Note: "recorded the Operator answer"})
	if detail := f.show(t, task.ResourceID); detail.NeedsYou || detail.Progress != "waiting" || detail.Result != "whole Task completed" {
		t.Fatal("attention resolution changed progress/history")
	}

	report := strings.Repeat("r", MaxTaskTextBytes)
	for range 2 {
		task = f.change(t, 1, "report", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Report: &report})
	}

	var reports int
	if err := s.db.QueryRow("SELECT count(*) FROM task_content WHERE task_id=? AND kind='report'", task.ResourceID).Scan(&reports); err != nil || reports != 1 {
		t.Fatal("unchanged report body duplicated")
	}

	task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[0].ResourceID})
	_, err = s.ChangeSession(ctx, f.op.Token, "appoint", SessionChangeRequest{Context: f.ref, OperationID: opaqueID(), ExpectedRevision: 2, AgentID: f.agents[2].ResourceID})
	if err != nil {
		t.Fatal(err)
	}

	title := "old role cannot edit"
	_, err = s.ChangeTask(ctx, f.creds[0].Token, "edit", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Title: &title})
	reviewCode(t, err, "forbidden")
	task = f.change(t, 0, "report", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, Report: &report})
	task = f.change(t, 2, "unassign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[1].ResourceID})
	_, err = s.ChangeTask(ctx, f.creds[1].Token, "status", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "working"})
	reviewCode(t, err, "forbidden")

	if f.show(t, task.ResourceID).ID != task.ResourceID {
		t.Fatal("removed assignee lost ordinary metadata read")
	}
}

func TestTaskSpecGraphDirectionAndCycleRace(t *testing.T) {
	ctx := context.Background()
	f := taskReviewSetup(t)

	s := f.service.Store
	var tasks [4]Receipt
	for i := range tasks {
		tasks[i] = f.create(t, fmt.Sprintf("branch %d", i))
	}

	add := func(from, to int) {
		t.Helper()

		r := DependencyRequest{Context: f.ref, OperationID: opaqueID(), ExpectedGraphRevision: f.snapshot(t).Session.GraphRevision, PrerequisiteID: tasks[from].ResourceID, DependentID: tasks[to].ResourceID}
		first, err := s.ChangeDependency(ctx, f.creds[0].Token, "add", r)
		if err != nil {
			t.Fatal(err)
		}

		retry, err := s.ChangeDependency(ctx, f.creds[0].Token, "add", r)
		if err != nil || retry != first {
			t.Fatal("edge retry duplicated or changed receipt")
		}
	}
	add(0, 1)
	add(0, 2)
	add(1, 3)
	add(2, 3)

	page, err := s.SnapshotPage(ctx, f.creds[1].Token, SnapshotPageRequest{Context: f.ref, Kind: "dependencies", TaskID: tasks[1].ResourceID})
	if err != nil || len(page.Items) != 1 || page.Items[0].(Dependency).PrerequisiteID != tasks[0].ResourceID {
		t.Fatal("dependency arrow direction reversed")
	}

	tasks[0] = f.change(t, 0, "assign", TaskChangeRequest{TaskID: tasks[0].ResourceID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
	tasks[0] = f.change(t, 1, "status", TaskChangeRequest{TaskID: tasks[0].ResourceID, ExpectedRevision: tasks[0].Revision, Progress: "cancelled", Reason: "prerequisite abandoned"})
	if detail := f.show(t, tasks[1].ResourceID); detail.Progress != "waiting" || detail.CancelledPrerequisiteCount != 1 {
		t.Fatal("cancelled prerequisite cascaded or disappeared")
	}

	tasks[3] = f.change(t, 0, "assign", TaskChangeRequest{TaskID: tasks[3].ResourceID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
	f.change(t, 1, "status", TaskChangeRequest{TaskID: tasks[3].ResourceID, ExpectedRevision: tasks[3].Revision, Progress: "finished", Summary: "complete despite waiting prerequisites"})
	graph := f.snapshot(t).Session.GraphRevision
	type outcome struct {
		request DependencyRequest
		err     error
	}

	results := make(chan outcome, 2)
	start := make(chan struct{})
	for _, pair := range [][2]int{{1, 2}, {2, 1}} {
		r := DependencyRequest{Context: f.ref, OperationID: opaqueID(), ExpectedGraphRevision: graph, PrerequisiteID: tasks[pair[0]].ResourceID, DependentID: tasks[pair[1]].ResourceID}
		go func(r DependencyRequest) {
			<-start
			_, e := s.ChangeDependency(ctx, f.creds[0].Token, "add", r)
			results <- outcome{r, e}
		}(r)
	}

	close(start)

	winners := 0
	var loser DependencyRequest
	for range 2 {
		result := <-results
		if result.err == nil {
			winners++
		} else {
			reviewCode(t, result.err, "conflict")
			loser = result.request
		}
	}

	if winners != 1 {
		t.Fatal("reciprocal edge race did not serialize")
	}

	loser.ExpectedGraphRevision = f.snapshot(t).Session.GraphRevision
	_, err = s.ChangeDependency(ctx, f.creds[0].Token, "add", loser)
	reviewCode(t, err, "conflict")

	var edges, history int
	if err := s.db.QueryRow("SELECT count(*) FROM dependencies").Scan(&edges); err != nil {
		t.Fatal(err)
	}

	if err := s.db.QueryRow("SELECT count(*) FROM graph_history").Scan(&history); err != nil || edges != 5 || history != 5 {
		t.Fatal("cycle/retry left extra edges or history")
	}
}

func TestTaskSpecAssignmentRemovalRace(t *testing.T) {
	ctx := context.Background()
	f := taskReviewSetup(t)
	s := f.service.Store
	task := f.create(t, "assignment is a shared revision")
	task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
	start := make(chan struct{})
	removed := make(chan error, 1)
	written := make(chan error, 1)
	go func() {
		<-start
		_, err := s.ChangeTask(ctx, f.creds[0].Token, "unassign", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.agents[1].ResourceID})
		removed <- err
	}()
	go func() {
		<-start
		_, err := s.ChangeTask(ctx, f.creds[1].Token, "status", TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "working"})
		written <- err
	}()
	close(start)

	removeErr, writeErr := <-removed, <-written
	current := f.show(t, task.ResourceID)
	if removeErr == nil {
		reviewCode(t, writeErr, "forbidden")

		if !current.Unassigned || current.Progress != "waiting" {
			t.Fatal("removed assignee wrote after removal")
		}
	} else {
		reviewCode(t, removeErr, "conflict")

		if writeErr != nil || current.Unassigned || current.Progress != "working" {
			t.Fatal("competing write silently lost assignment/revision")
		}
	}

	if current.Revision != task.Revision+1 {
		t.Fatal("competing assignment/write advanced revision twice")
	}
}

func TestTaskSpecClosureLinearizationAndReplay(t *testing.T) {
	ctx := context.Background()
	f := taskReviewSetup(t)
	s := f.service.Store
	task := f.create(t, "unfinished at closure")
	task = f.change(t, 0, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
	report := "persisted before closure"
	reportRequest := TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Report: &report}

	saved, err := s.ChangeTask(ctx, f.creds[1].Token, "report", reportRequest)
	if err != nil {
		t.Fatal(err)
	}

	task = saved
	// Observe the actual Session state at every Task write; do not enforce it in
	// this trigger, so a write after closure would remain visible to the test.
	_, err = s.db.Exec(`CREATE TABLE review_write_audit(closed INTEGER); CREATE TRIGGER review_task_write AFTER UPDATE ON tasks BEGIN INSERT INTO review_write_audit SELECT closed FROM sessions WHERE id=NEW.session_id; END;`)
	if err != nil {
		t.Fatal(err)
	}

	r := TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: task.Revision, Progress: "working"}
	start := make(chan struct{})
	type outcome struct {
		r   Receipt
		err error
	}
	write := make(chan outcome, 1)
	closed := make(chan outcome, 1)
	go func() {
		<-start
		receipt, e := s.ChangeTask(ctx, f.creds[1].Token, "status", r)
		write <- outcome{receipt, e}
	}()
	go func() {
		<-start
		receipt, e := s.ChangeSession(ctx, f.op.Token, "close", SessionChangeRequest{Context: f.ref, OperationID: opaqueID(), ExpectedRevision: 2})
		closed <- outcome{receipt, e}
	}()
	close(start)

	changed, closure := <-write, <-closed
	if closure.err != nil {
		t.Fatal(closure.err)
	}

	recovered, err := s.ChangeTask(ctx, f.creds[1].Token, "report", reportRequest)
	if err != nil || recovered != saved {
		t.Fatal("closed Session lost preexisting committed receipt")
	}

	if changed.err != nil {
		reviewCode(t, changed.err, "session_closed")
	} else {
		retry, err := s.ChangeTask(ctx, f.creds[1].Token, "status", r)
		if err != nil || retry != changed.r {
			t.Fatal("closed Session lost authorized prior receipt")
		}
	}

	var afterClose int
	if err := s.db.QueryRow("SELECT count(*) FROM review_write_audit WHERE closed=1").Scan(&afterClose); err != nil || afterClose != 0 {
		t.Fatal("Task mutation committed while Session closed")
	}

	before := f.show(t, task.ResourceID)
	for _, action := range []string{"status", "report", "attention.set", "reference.add", "edit", "unassign"} {
		actor := 1
		if action == "edit" || action == "unassign" {
			actor = 0
		}

		_, err := s.ChangeTask(ctx, f.creds[actor].Token, action, TaskChangeRequest{Context: f.ref, OperationID: opaqueID(), TaskID: task.ResourceID, ExpectedRevision: before.Revision})
		reviewCode(t, err, "session_closed")
	}

	if after := f.show(t, task.ResourceID); after.Progress != before.Progress || after.Revision != before.Revision {
		t.Fatal("closed mutations changed unfinished work")
	}
}

func TestTaskSpecBoundedPinnedPrivateReads(t *testing.T) {
	ctx := context.Background()

	f := taskReviewSetup(t)
	s := f.service.Store
	for i := range 1000 {
		f.create(t, fmt.Sprintf("retained Task %04d", i))
	}

	snapshot := f.snapshot(t)
	if snapshot.Source == "" || snapshot.CapturedAt == "" || snapshot.Counts.Tasks != 1000 {
		t.Fatal("snapshot omitted retained Tasks or provenance")
	}

	unchanged, err := s.Snapshot(ctx, f.op.Token, SnapshotRequest{Context: f.ref, KnownRevision: snapshot.Session.ReadRevision})
	if err != nil || !unchanged.Unchanged || unchanged.Counts != nil {
		t.Fatal("conditional snapshot wrote or rebuilt unchanged state")
	}

	request := SnapshotPageRequest{Context: f.ref, Kind: "tasks", ReadRevision: snapshot.Session.ReadRevision, Limit: 100}

	seen := map[string]bool{}
	firstCursor := ""
	for {
		page, err := s.SnapshotPage(ctx, f.creds[1].Token, request)
		if err != nil {
			t.Fatal(err)
		}

		if firstCursor == "" {
			firstCursor = page.Next
		}

		for _, item := range page.Items {
			task := item.(Task)
			if seen[task.ID] {
				t.Fatal("Task page repeated an ID")
			}

			seen[task.ID] = true
		}

		if page.Next == "" {
			break
		}

		request.After = page.Next
	}

	if len(seen) != 1000 || firstCursor == "" {
		t.Fatal("paging silently omitted retained Tasks")
	}

	var taskID string
	for id := range seen {
		taskID = id
		break
	}

	f.change(t, 0, "assign", TaskChangeRequest{TaskID: taskID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
	request.After = firstCursor
	_, err = s.SnapshotPage(ctx, f.creds[1].Token, request)
	reviewCode(t, err, "conflict")
	large := strings.Repeat("\x00", MaxTaskTextBytes)

	created, err := s.CreateTask(ctx, f.creds[0].Token, TaskCreateRequest{Context: f.ref, OperationID: opaqueID(), Title: "large detail", Description: large})
	if err != nil {
		t.Fatal(err)
	}

	created = f.change(t, 0, "assign", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: 1, AgentID: f.agents[1].ResourceID})
	f.change(t, 1, "report", TaskChangeRequest{TaskID: created.ResourceID, ExpectedRevision: created.Revision, Report: &large})

	client, err := NewClient(f.service.lock.dir, "", OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	wire, err := client.Do(ctx, "POST", "/v1/task/show", TaskReadRequest{Context: f.ref, TaskID: created.ResourceID})
	if err != nil || len(wire) > MaxRequestBytes {
		t.Fatalf("large valid detail is unreadable: %v", err)
	}

	var detail struct{ Data TaskDetail }
	if json.Unmarshal(wire, &detail) != nil || detail.Data.Description != large || detail.Data.Report != large {
		t.Fatal("large detail truncated")
	}

	history := SnapshotPageRequest{Context: f.ref, Kind: "history", TaskID: created.ResourceID, ReadRevision: detail.Data.ReadRevision, Limit: 100}

	entries := 0
	pages := 0
	for {
		wire, err := client.Do(ctx, "POST", "/v1/task/history", history)
		if err != nil || len(wire) > MaxRequestBytes {
			t.Fatalf("bounded history unreadable: %v", err)
		}

		var page struct{ Data SnapshotPage }
		if json.Unmarshal(wire, &page) != nil {
			t.Fatal("invalid history JSON")
		}

		entries += len(page.Data.Items)
		pages++

		if page.Data.Next == "" {
			break
		}

		wrongTask := history
		wrongTask.TaskID = taskID
		wrongTask.After = page.Data.Next
		_, err = s.SnapshotPage(ctx, f.op.Token, wrongTask)
		reviewCode(t, err, "invalid_input")
		history.After = page.Data.Next
	}

	if entries != 3 || pages < 2 {
		t.Fatal("history byte pagination lost entries")
	}

	private := &RuntimeBinding{InstanceID: "snapshot-private-instance", Endpoint: "snapshot-private-endpoint", PaneID: "snapshot-private-pane"}
	if _, err := s.BindAgent(ctx, f.creds[0].Token, "attach", AgentBindingRequest{Context: f.ref, OperationID: opaqueID(), AgentID: f.agents[0].ResourceID, ExpectedRevision: 1, Runtime: private}); err != nil {
		t.Fatal(err)
	}

	page, err := s.SnapshotPage(ctx, f.op.Token, SnapshotPageRequest{Context: f.ref, Kind: "agents"})
	if err != nil {
		t.Fatal(err)
	}

	encoded, _ := json.Marshal(page)
	for _, secret := range []string{private.InstanceID, private.Endpoint, private.PaneID, f.op.Token, f.creds[0].Token, tokenHash(f.creds[0].Token)} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("snapshot leaked private identity/credential")
		}
	}

	browser := issueTest(t, s, BrowserCapability, opaqueID(), f.ref.SessionID)
	browserFile := filepath.Join(f.service.lock.dir, "browser.json")
	if err := writePrivate(browserFile, browser, false); err != nil {
		t.Fatal(err)
	}

	view, err := NewClient(f.service.lock.dir, browserFile, BrowserCapability)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := view.Do(ctx, "POST", "/v1/session/snapshot", SnapshotRequest{Context: f.ref}); err != nil {
		t.Fatal(err)
	}

	_, err = view.Do(ctx, "POST", "/v1/task/create", TaskCreateRequest{Context: f.ref, OperationID: opaqueID(), Title: "browser cannot write"})
	reviewCode(t, err, "forbidden")
	t.Logf("retained Tasks=%d; escaped detail=%d bytes; history pages=%d", len(seen), len(wire), pages)
}
