package stack

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

// undoMigration9 turns a current database back into the schema 8 layout of
// v0.1.6: no standing orders, and Task mail always from an Agent.
const undoMigration9 = `PRAGMA foreign_keys=OFF;
DROP TABLE workspace_orders;
DROP TRIGGER mail_mentions_task;
DROP TRIGGER mail_mentions_task_change;
CREATE TABLE mail_messages_v8(
 id TEXT PRIMARY KEY NOT NULL, task_id TEXT NOT NULL REFERENCES tasks(id), sequence INTEGER NOT NULL CHECK(sequence>0),
 sender_id TEXT NOT NULL REFERENCES agents(id), received_at TEXT NOT NULL, body TEXT NOT NULL,
 UNIQUE(task_id,sequence)
);
INSERT INTO mail_messages_v8 SELECT id,task_id,sequence,sender_id,received_at,body FROM mail_messages ORDER BY task_id,sequence;
DROP TABLE mail_messages;
ALTER TABLE mail_messages_v8 RENAME TO mail_messages;
CREATE TRIGGER mail_mentions_task BEFORE INSERT ON mail_mentions
 WHEN NEW.task_id IS NOT (SELECT task_id FROM mail_messages WHERE id=NEW.message_id)
 BEGIN SELECT RAISE(ABORT,'FOREIGN KEY constraint failed'); END;
CREATE TRIGGER mail_mentions_task_change BEFORE UPDATE OF message_id,task_id ON mail_mentions
 WHEN NEW.task_id IS NOT (SELECT task_id FROM mail_messages WHERE id=NEW.message_id)
 BEGIN SELECT RAISE(ABORT,'FOREIGN KEY constraint failed'); END;
PRAGMA user_version=8;
`

// TestMigration9KeepsAgentMailAndAllowsOperatorMail migrates a schema 8
// (v0.1.6) database with Task mail. Every read of the mail stays the same
// byte for byte, the copy is made first, and afterwards a message can come
// from the Operator, with no Agent as sender.
func TestMigration9KeepsAgentMailAndAllowsOperatorMail(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	var tasks []string
	for _, title := range []string{"First", "Second"} {
		id := f.create(t, title).ResourceID
		tasks = append(tasks, id)
		for _, agent := range []string{f.aID, f.bID} {
			f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: f.show(t, id).Revision, AgentID: agent})
		}
		for _, body := range []string{"Short note for @03-worker", strings.Repeat("Long mail. ", 300) + "@03-worker", "No mention"} {
			if _, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: id, OperationID: opaqueID(), Body: body}); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := schema8Reads(t, f, tasks)
	path, state := s.path, f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration9)

	f.service = serveTest(t, state, "127.0.0.1:0")
	s = f.service.Store
	if countTest(t, s, "PRAGMA user_version") != 9 || countTest(t, s, "PRAGMA foreign_keys") != 1 {
		t.Fatal("not migrated, or foreign keys stay off")
	}
	if _, err := os.Stat(path + ".pre-v8"); err != nil {
		t.Fatal("no copy before the migration:", err)
	}
	if !strings.Contains(s.Migrated, "from schema 8 to 9") {
		t.Fatal("migration line:", s.Migrated)
	}
	if after := schema8Reads(t, f, tasks); after != before {
		t.Fatalf("reads changed:\nbefore %s\nafter  %s", before, after)
	}
	if countTest(t, s, "SELECT count(*) FROM mail_messages WHERE sender_kind='agent' AND sender_id IS NOT NULL") != 6 || countTest(t, s, "SELECT count(*) FROM workspace_orders") != 0 {
		t.Fatal("mail lost its sender, or orders appeared")
	}
	var message string
	if err := s.db.QueryRow("SELECT id FROM mail_messages WHERE task_id=? AND sequence=1", tasks[0]).Scan(&message); err != nil {
		t.Fatal(err)
	}
	insert := "INSERT INTO mail_messages(id,task_id,sequence,sender_kind,sender_id,received_at,body) VALUES"
	for name, statement := range map[string]string{
		"Operator mail with an Agent sender":   insert + "('op-1','" + tasks[0] + "',4,'operator','" + f.aID + "','now','x')",
		"Agent mail without a sender":          insert + "('op-1','" + tasks[0] + "',4,'agent',NULL,'now','x')",
		"another sender kind":                  insert + "('op-1','" + tasks[0] + "',4,'browser',NULL,'now','x')",
		"mail from no Agent":                   insert + "('op-1','" + tasks[0] + "',4,'agent','missing','now','x')",
		"mention of a message of another Task": "INSERT INTO mail_mentions VALUES('" + message + "','" + tasks[1] + "','" + f.aID + "','self','now')",
		"mention moved to another Task":        "UPDATE mail_mentions SET task_id='" + tasks[1] + "' WHERE message_id='" + message + "'",
	} {
		if _, err := s.db.Exec(statement); err == nil || !strings.Contains(err.Error(), "constraint failed") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// What a-stack answer stores: Operator mail that mentions an assignee.
	if _, err := s.db.Exec(insert+"('op-1',?,4,'operator',NULL,'now','The answer');INSERT INTO mail_mentions VALUES('op-1',?,?,'pending','now')", tasks[0], tasks[0], f.bID); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: tasks[0]})
	if err != nil {
		t.Fatal(err)
	}
	last := page.Messages[len(page.Messages)-1]
	if last.SenderKind != "operator" || last.SenderID != "" || last.SenderName != "Operator" || last.Body != "The answer" || page.Messages[0].SenderKind != "" {
		t.Fatalf("messages %+v", page.Messages)
	}
	// Agents still send mail after the rebuild.
	if _, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: tasks[0], OperationID: opaqueID(), Body: "After @03-worker"}); err != nil {
		t.Fatal(err)
	}
}

// A schema 8 database with a broken mail reference is not migrated.
func TestMigration9RefusesBrokenReferences(t *testing.T) {
	f := tasksFixture(t)
	id := f.create(t, "Task").ResourceID
	path, state := f.service.Store.path, f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	execFile(t, path, undoMigration9+"INSERT INTO mail_messages VALUES('orphan','"+id+"',1,'missing-agent','now','x')")
	if _, err := Start(ctx, state, "127.0.0.1:0"); err == nil || !strings.Contains(err.Error(), "foreign_key_check") {
		t.Fatalf("start = %v", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 8 {
		t.Fatalf("version %d after a refused migration", version)
	}
}
