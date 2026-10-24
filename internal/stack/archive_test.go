package stack

import (
	"database/sql"
	"testing"
	"time"
)

// undoMigration6 turns a current database back into the v5 shape, so upgrade
// tests can start from an older schema.
const undoMigration6 = `
CREATE TABLE task_history_v5(sequence INTEGER PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),actor_id TEXT NOT NULL REFERENCES agents(id),action TEXT NOT NULL,revision INTEGER NOT NULL,received_at TEXT NOT NULL,changes TEXT NOT NULL);
INSERT INTO task_history_v5 SELECT sequence,task_id,actor_id,action,revision,received_at,changes FROM task_history;
DROP TABLE task_history;
ALTER TABLE task_history_v5 RENAME TO task_history;
CREATE INDEX task_history_task ON task_history(task_id,sequence);
ALTER TABLE tasks DROP COLUMN archived_at;
ALTER TABLE tasks DROP COLUMN ended_at;
`

func (f taskTeam) assignA(t *testing.T, id string) {
	t.Helper()
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: f.show(t, id).Revision, AgentID: f.aID})
}
func (f taskTeam) progress(t *testing.T, id, progress string) {
	t.Helper()
	r := TaskChangeRequest{TaskID: id, ExpectedRevision: f.show(t, id).Revision, Progress: progress}
	switch progress {
	case "finished":
		r.Summary = "Done"
	case "cancelled":
		r.Reason = "Dropped"
	}
	f.change(t, f.a.Token, "status", r)
}
func (f taskTeam) depend(t *testing.T, from, to string) {
	t.Helper()
	snapshot, err := f.service.Store.Snapshot(ctx, f.operator.Token, SnapshotRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Store.ChangeDependency(ctx, f.orch.Token, "add", DependencyRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedGraphRevision: snapshot.Session.GraphRevision, PrerequisiteID: from, DependentID: to}); err != nil {
		t.Fatal(err)
	}
}
func (f taskTeam) lastHistory(t *testing.T, id string) TaskHistoryEntry {
	t.Helper()
	page, err := f.service.Store.SnapshotPage(ctx, f.operator.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "history", TaskID: id, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items[len(page.Items)-1].(TaskHistoryEntry)
}
func archiveState(task TaskDetail) string {
	if !task.Archived {
		return "visible"
	}
	return task.ArchivedReason
}

func TestArchiveCommandAccessHistoryAndReopen(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	id := f.create(t, "Side work").ResourceID
	f.assignA(t, id)
	archive := func(token, action string, revision int64) error {
		_, err := s.ChangeTask(ctx, token, action, TaskChangeRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, ExpectedRevision: revision})
		return err
	}
	requireCode(t, archive(f.orch.Token, "archive", f.show(t, id).Revision), "conflict") // still Waiting
	f.progress(t, id, "finished")
	done := f.show(t, id)
	if done.EndedAt == "" || done.Archived || done.ArchivedAt != "" {
		t.Fatal("a finished Task without dependents must stay visible", done.Task)
	}
	requireCode(t, archive(f.a.Token, "archive", done.Revision), "forbidden") // assignee, not the Orchestrator
	browser := issueTest(t, s, BrowserCapability, "archive-view", f.sessions[0])
	requireCode(t, archive(browser.Token, "archive", done.Revision), "forbidden")
	if _, err := clientForCredential(t, f, browser).Do(ctx, "POST", "/v1/task/archive", TaskChangeRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, ExpectedRevision: done.Revision}); err == nil {
		t.Fatal("browser reached the archive endpoint")
	}
	requireCode(t, archive(f.orch.Token, "archive", done.Revision-1), "conflict")
	_, err := s.ChangeTask(ctx, f.orch.Token, "archive", TaskChangeRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, ExpectedRevision: done.Revision, Reason: "no"})
	requireCode(t, err, "invalid_input")

	request := TaskChangeRequest{Context: f.ref(0), OperationID: "archive-side-work", TaskID: id, ExpectedRevision: done.Revision}
	receipt, err := s.ChangeTask(ctx, f.orch.Token, "archive", request)
	if err != nil || receipt.Revision != done.Revision+1 || receipt.Outcome != "archive" {
		t.Fatal(receipt, err)
	}
	if replay, err := s.ChangeTask(ctx, f.orch.Token, "archive", request); err != nil || replay != receipt {
		t.Fatal("archive retry changed its receipt", err)
	}
	archived := f.show(t, id)
	if archiveState(archived) != "command" || archived.ArchivedAt == "" || archived.LastAction != "task.archive" {
		t.Fatal("archive", archived.Task)
	}
	if h := f.lastHistory(t, id); h.Action != "task.archive" || h.ActorKind != "agent" || h.ActorID != f.orchID {
		t.Fatal("archive history", h)
	}
	requireCode(t, archive(f.orch.Token, "archive", archived.Revision), "conflict")

	// The Operator can archive and unarchive too; the history names the Operator,
	// and updated_by keeps the last Agent.
	if err = archive(f.operator.Token, "unarchive", archived.Revision); err != nil {
		t.Fatal(err)
	}
	visible := f.show(t, id)
	if archiveState(visible) != "visible" || visible.ArchivedAt != "" || visible.UpdatedBy != f.orchID {
		t.Fatal("unarchive", visible.Task)
	}
	if h := f.lastHistory(t, id); h.Action != "task.unarchive" || h.ActorKind != "operator" || h.ActorID != "" {
		t.Fatal("Operator history", h)
	}
	requireCode(t, archive(f.operator.Token, "unarchive", visible.Revision), "conflict")
	if err = archive(f.operator.Token, "archive", visible.Revision); err != nil {
		t.Fatal(err)
	}

	// Reopen clears the archive and the end time.
	f.change(t, f.a.Token, "reopen", TaskChangeRequest{TaskID: id, ExpectedRevision: visible.Revision + 1, Reason: "More to do"})
	reopened := f.show(t, id)
	if archiveState(reopened) != "visible" || reopened.ArchivedAt != "" || reopened.EndedAt != "" {
		t.Fatal("reopen kept the archive", reopened.Task)
	}
	requireCode(t, archive(f.orch.Token, "archive", reopened.Revision), "conflict")
}

func TestArchiveChainRule(t *testing.T) {
	f := tasksFixture(t)
	ids := map[string]string{}
	for _, name := range []string{"A", "B", "C", "D"} {
		ids[name] = f.create(t, name).ResourceID
		f.assignA(t, ids[name])
	}
	// A → B → C, and A → D.
	f.depend(t, ids["A"], ids["B"])
	f.depend(t, ids["B"], ids["C"])
	f.depend(t, ids["A"], ids["D"])
	expect := func(want map[string]string) {
		t.Helper()
		for name, state := range want {
			if got := archiveState(f.show(t, ids[name])); got != state {
				t.Fatalf("%s: got %s, want %s", name, got, state)
			}
		}
	}
	f.progress(t, ids["A"], "finished")
	f.progress(t, ids["B"], "finished")
	f.progress(t, ids["C"], "working")
	// Mixed chain: A unblocks a waiting D, and B unblocks a working C.
	expect(map[string]string{"A": "visible", "B": "visible", "C": "visible", "D": "visible"})
	f.progress(t, ids["C"], "finished")
	expect(map[string]string{"A": "visible", "B": "chain", "C": "visible"})
	f.progress(t, ids["D"], "cancelled")
	// Every dependent is done: A and B leave; C and D end their chains and stay.
	expect(map[string]string{"A": "chain", "B": "chain", "C": "visible", "D": "visible"})
	// The command wins over the chain rule.
	f.change(t, f.orch.Token, "archive", TaskChangeRequest{TaskID: ids["B"], ExpectedRevision: f.show(t, ids["B"]).Revision})
	expect(map[string]string{"B": "command"})
	f.change(t, f.orch.Token, "archive", TaskChangeRequest{TaskID: ids["C"], ExpectedRevision: f.show(t, ids["C"]).Revision})
	expect(map[string]string{"C": "command"})
	// Reopening C brings back B, whose dependent is live again.
	f.change(t, f.a.Token, "reopen", TaskChangeRequest{TaskID: ids["C"], ExpectedRevision: f.show(t, ids["C"]).Revision, Reason: "Regression"})
	f.change(t, f.orch.Token, "unarchive", TaskChangeRequest{TaskID: ids["B"], ExpectedRevision: f.show(t, ids["B"]).Revision})
	expect(map[string]string{"A": "chain", "B": "visible", "C": "visible", "D": "visible"})

	// The Task list says the same as task show.
	page, err := f.service.Store.SnapshotPage(ctx, f.operator.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		task := item.(Task)
		if task.ID == ids["A"] && (!task.Archived || task.ArchivedReason != "chain") || task.ID == ids["B"] && task.Archived {
			t.Fatal("task list disagrees with task show", task)
		}
	}
}

func TestArchiveAgeRuleWithFixedClock(t *testing.T) {
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	at := func(d time.Duration) {
		moment := start.Add(d)
		fixedClock.Store(&moment)
	}
	t.Cleanup(func() { fixedClock.Store(nil) })
	at(0)
	f := tasksFixture(t)
	done := f.create(t, "Done long ago").ResourceID
	open := f.create(t, "Still open").ResourceID
	f.assignA(t, done)
	f.assignA(t, open)
	f.progress(t, done, "finished")
	f.progress(t, open, "working")
	if got := f.show(t, done).EndedAt; got != start.Format(time.RFC3339Nano) {
		t.Fatal("ended_at", got)
	}
	at(ArchiveAge - time.Second)
	if archiveState(f.show(t, done)) != "visible" {
		t.Fatal("archived before the age limit")
	}
	at(ArchiveAge)
	if archiveState(f.show(t, done)) != "age" || archiveState(f.show(t, open)) != "visible" {
		t.Fatal("age rule")
	}
	// The command wins over age; reopen and finish again restart the clock.
	f.change(t, f.orch.Token, "archive", TaskChangeRequest{TaskID: done, ExpectedRevision: f.show(t, done).Revision})
	if archiveState(f.show(t, done)) != "command" {
		t.Fatal("command must win over age")
	}
	f.change(t, f.a.Token, "reopen", TaskChangeRequest{TaskID: done, ExpectedRevision: f.show(t, done).Revision, Reason: "Again"})
	f.progress(t, done, "cancelled")
	if archiveState(f.show(t, done)) != "visible" {
		t.Fatal("a new end must restart the age clock")
	}
	at(2*ArchiveAge + time.Hour)
	if archiveState(f.show(t, done)) != "age" {
		t.Fatal("cancelled Tasks age out too")
	}
}

func TestArchiveMigrationFromV5(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "upgrade", true: "rollback"}[fail], func(t *testing.T) {
			f := tasksFixture(t)
			finished, cancelled, waiting := f.create(t, "Finished").ResourceID, f.create(t, "Cancelled").ResourceID, f.create(t, "Waiting").ResourceID
			for _, id := range []string{finished, cancelled, waiting} {
				f.assignA(t, id)
			}
			f.progress(t, finished, "finished")
			f.progress(t, cancelled, "cancelled")
			report := "Report after the end"
			f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: finished, ExpectedRevision: f.show(t, finished).Revision, Report: &report})
			path, state := f.service.Store.path, f.service.lock.dir
			if err := f.service.Close(ctx); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			var endedFinished, endedCancelled string
			var entries int
			if db.QueryRow("SELECT received_at FROM task_history WHERE task_id=? AND action='task.status' ORDER BY sequence DESC LIMIT 1", finished).Scan(&endedFinished) != nil ||
				db.QueryRow("SELECT received_at FROM task_history WHERE task_id=? AND action='task.status' ORDER BY sequence DESC LIMIT 1", cancelled).Scan(&endedCancelled) != nil ||
				db.QueryRow("SELECT count(*) FROM task_history").Scan(&entries) != nil {
				t.Fatal("history before the downgrade")
			}
			if _, err = db.Exec(undoMigration6 + "PRAGMA user_version=5"); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err = db.Exec("CREATE TABLE task_history_v6(sentinel TEXT); INSERT INTO task_history_v6 VALUES('keep-v5')"); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			if fail {
				if s, err := Start(ctx, state, "127.0.0.1:0"); err == nil {
					s.Close(ctx)
					t.Fatal("colliding migration accepted")
				}
				db, _ = sql.Open("sqlite3", path)
				var version, after int
				var sentinel string
				if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 5 || db.QueryRow("SELECT count(*) FROM task_history").Scan(&after) != nil || after != entries ||
					db.QueryRow("SELECT sentinel FROM task_history_v6").Scan(&sentinel) != nil || sentinel != "keep-v5" {
					t.Fatal("failed v6 migration damaged the v5 database")
				}
				if _, err = db.Exec("DROP TABLE task_history_v6"); err != nil {
					t.Fatal(err)
				}
				db.Close()
			}
			service, err := Start(ctx, state, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close(ctx)
			f.service = service
			if countTest(t, service.Store, "PRAGMA user_version") != schemaVersion || countTest(t, service.Store, "SELECT count(*) FROM task_history WHERE actor_kind='agent' AND actor_id IS NOT NULL") != entries {
				t.Fatal("v6 migration lost history")
			}
			// The end time comes from the status history, not the later report.
			if got := f.show(t, finished); got.EndedAt != endedFinished || got.Archived {
				t.Fatal("finished backfill", got.Task)
			}
			if got := f.show(t, cancelled); got.EndedAt != endedCancelled || got.Archived {
				t.Fatal("cancelled backfill", got.Task)
			}
			if got := f.show(t, waiting); got.EndedAt != "" || got.ArchivedAt != "" {
				t.Fatal("waiting Task got an end time", got.Task)
			}
			// Agent history keeps its foreign key; the Operator row has no actor ID.
			if _, err = service.Store.db.Exec("INSERT INTO task_history(task_id,actor_kind,actor_id,action,revision,received_at,changes) VALUES(?,'agent','no-such-agent','x',1,'','{}')", waiting); err == nil {
				t.Fatal("history accepted an unknown Agent")
			}
			if _, err = service.Store.db.Exec("INSERT INTO task_history(task_id,actor_kind,actor_id,action,revision,received_at,changes) VALUES(?,'operator',?,'x',1,'','{}')", waiting, f.aID); err == nil {
				t.Fatal("an Operator entry accepted an actor ID")
			}
			f.change(t, f.orch.Token, "archive", TaskChangeRequest{TaskID: finished, ExpectedRevision: f.show(t, finished).Revision})
			if archiveState(f.show(t, finished)) != "command" {
				t.Fatal("archive after the upgrade")
			}
		})
	}
}
