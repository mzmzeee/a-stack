package stack

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// undoMigration8 rebuilds Task texts and Task mail in their schema 7 layout:
// WITHOUT ROWID tables, UNIQUE(id,task_id) and the composite foreign key.
const undoMigration8 = `PRAGMA foreign_keys=OFF;
CREATE TABLE task_content_v7(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),kind TEXT NOT NULL,body TEXT NOT NULL,actor_id TEXT NOT NULL REFERENCES agents(id),received_at TEXT NOT NULL) WITHOUT ROWID;
INSERT INTO task_content_v7 SELECT * FROM task_content;
DROP TABLE task_content;
ALTER TABLE task_content_v7 RENAME TO task_content;
CREATE INDEX task_content_task ON task_content(task_id,id);
CREATE TABLE mail_messages_v7(
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), sequence INTEGER NOT NULL CHECK(sequence>0),
 sender_id TEXT NOT NULL REFERENCES agents(id), received_at TEXT NOT NULL, body TEXT NOT NULL,
 UNIQUE(task_id,sequence), UNIQUE(id,task_id)
) WITHOUT ROWID;
INSERT INTO mail_messages_v7 SELECT * FROM mail_messages;
CREATE TABLE mail_mentions_v7(
 message_id TEXT NOT NULL, task_id TEXT NOT NULL, recipient_id TEXT NOT NULL REFERENCES agents(id),
 notice_state TEXT NOT NULL CHECK(notice_state IN ('pending','submitting','submitted','uncertain','suppressed','self')),
 state_at TEXT NOT NULL,
 PRIMARY KEY(message_id,recipient_id), FOREIGN KEY(message_id,task_id) REFERENCES mail_messages(id,task_id)
) WITHOUT ROWID;
INSERT INTO mail_mentions_v7 SELECT * FROM mail_mentions;
DROP TABLE mail_mentions;
DROP TABLE mail_messages;
ALTER TABLE mail_messages_v7 RENAME TO mail_messages;
ALTER TABLE mail_mentions_v7 RENAME TO mail_mentions;
CREATE INDEX mail_mentions_pending ON mail_mentions(notice_state,recipient_id,message_id);
CREATE INDEX mail_mentions_membership ON mail_mentions(task_id,recipient_id,notice_state,message_id);
PRAGMA user_version=7;
`

// schema8Reads is every read that shows Task texts or Task mail, as JSON.
func schema8Reads(t *testing.T, f taskTeam, tasks []string) string {
	t.Helper()
	s := f.service.Store
	var out []any
	for _, id := range tasks {
		detail, err := s.ShowTask(ctx, f.operator.Token, TaskReadRequest{Context: f.ref(0), TaskID: id})
		if err != nil {
			t.Fatal(err)
		}
		history, err := s.SnapshotPage(ctx, f.operator.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "history", TaskID: id, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		mail, err := s.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: id})
		if err != nil {
			t.Fatal(err)
		}
		// Own mail reads now refresh reachability; compare retained content, not its read revision.
		detail.ReadRevision, history.ReadRevision = 0, 0
		out = append(out, detail, history, mail)
	}
	check, err := s.CheckMail(ctx, f.b.Token, MailCheckRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(append(out, check))
	return string(b)
}

func tableSQL(t *testing.T, s *Store, name string) string {
	t.Helper()
	var text string
	if err := s.db.QueryRow("SELECT sql FROM sqlite_schema WHERE name=?", name).Scan(&text); err != nil {
		t.Fatal(name, err)
	}
	return text
}

func TestMigration8RowidTablesKeepReadsAndConstraints(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	long := strings.Repeat("A realistic report line with some detail. ", 60) // about 2.5 KB
	var tasks []string
	for _, title := range []string{"First", "Second"} {
		id := f.create(t, title).ResourceID
		tasks = append(tasks, id)
		for _, agent := range []string{f.aID, f.bID} {
			f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: f.show(t, id).Revision, AgentID: agent})
		}
		f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: id, ExpectedRevision: f.show(t, id).Revision, Report: &long})
		for _, body := range []string{"Short note for @03-worker", long + " @03-worker", "No mention"} {
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
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(undoMigration9 + undoMigration8); err != nil {
		t.Fatal(err)
	}
	var old string
	if db.QueryRow("SELECT group_concat(sql) FROM sqlite_schema WHERE name IN ('task_content','mail_messages')").Scan(&old) != nil || strings.Count(old, "WITHOUT ROWID") != 2 || !strings.Contains(old, "UNIQUE(id,task_id)") {
		t.Fatal("no schema 7 layout:", old)
	}
	db.Close()

	f.service = serveTest(t, state, "127.0.0.1:0")
	s = f.service.Store
	if countTest(t, s, "PRAGMA user_version") != schemaVersion || countTest(t, s, "PRAGMA foreign_keys") != 1 {
		t.Fatal("not migrated, or foreign keys stay off")
	}
	if _, err = os.Stat(path + ".pre-v7"); err != nil {
		t.Fatal("no copy before the migration:", err)
	}
	for _, table := range []string{"task_content", "mail_messages"} {
		if strings.Contains(tableSQL(t, s, table), "WITHOUT ROWID") {
			t.Fatalf("%s is still WITHOUT ROWID", table)
		}
	}
	// The primary key, sequence and urgent indexes remain; UNIQUE(id,task_id) is gone.
	if n := countTest(t, s, "SELECT count(*) FROM pragma_index_list('mail_messages')"); n != 3 {
		t.Fatalf("mail_messages has %d indexes", n)
	}
	if after := schema8Reads(t, f, tasks); after != before {
		t.Fatalf("reads changed:\nbefore %s\nafter  %s", before, after)
	}
	// The constraints of schema 7 still hold.
	var message string
	if err = s.db.QueryRow("SELECT id FROM mail_messages WHERE task_id=? AND sequence=1", tasks[0]).Scan(&message); err != nil {
		t.Fatal(err)
	}
	for name, statement := range map[string]string{
		"mention of a message of another Task": "INSERT INTO mail_mentions VALUES('" + message + "','" + tasks[1] + "','" + f.aID + "','self','now')",
		"mention moved to another Task":        "UPDATE mail_mentions SET task_id='" + tasks[1] + "' WHERE message_id='" + message + "'",
		"mention of no message":                "INSERT INTO mail_mentions VALUES('missing','" + tasks[0] + "','" + f.aID + "','self','now')",
		"text without an ID":                   "INSERT INTO task_content VALUES(NULL,'" + tasks[0] + "','report','x','" + f.aID + "','now')",
		"message without an ID":                "INSERT INTO mail_messages(id,task_id,sequence,sender_id,received_at,body) VALUES(NULL,'" + tasks[0] + "',99,'" + f.aID + "','now','x')",
		"second message ID":                    "INSERT INTO mail_messages(id,task_id,sequence,sender_id,received_at,body) VALUES('" + message + "','" + tasks[1] + "',99,'" + f.aID + "','now','x')",
		"second sequence":                      "INSERT INTO mail_messages(id,task_id,sequence,sender_id,received_at,body) VALUES('new-id','" + tasks[0] + "',1,'" + f.aID + "','now','x')",
		"text of no Task":                      "INSERT INTO task_content VALUES('new-id','missing','report','x','" + f.aID + "','now')",
	} {
		if _, err = s.db.Exec(statement); err == nil || !strings.Contains(err.Error(), "constraint failed") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// New writes work in the new tables.
	if _, err = s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: tasks[0], OperationID: opaqueID(), Body: "After @03-worker"}); err != nil {
		t.Fatal(err)
	}
	f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: tasks[0], ExpectedRevision: f.show(t, tasks[0]).Revision, Report: new(string)})
	if countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE recipient_id='"+f.bID+"'") != 5 {
		t.Fatal("mention lost")
	}
}

// A database with a broken reference is not migrated, and serve stops.
func TestMigration8RefusesBrokenReferences(t *testing.T) {
	f := tasksFixture(t)
	f.create(t, "Task")
	path, state := f.service.Store.path, f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(undoMigration9 + undoMigration8 + "INSERT INTO task_content VALUES('orphan','missing-task','report','x','missing-agent','now')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = Start(ctx, state, "127.0.0.1:0")
	var e *Error
	if !errors.As(err, &e) || !e.DatabaseUnusable() || !strings.Contains(e.Message, "foreign_key_check") {
		t.Fatalf("start = %v", err)
	}
	db, _ = sql.Open("sqlite3", path)
	defer db.Close()
	var version int
	if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 7 {
		t.Fatalf("version %d after a refused migration", version)
	}
}
