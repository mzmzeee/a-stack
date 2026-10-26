package stack

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestPopulatedV3MailUpgradeAndFailureRecovery(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			dir := privateTestDir(t)
			path, err := ensureDatabaseFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_txlock=immediate")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			if _, err = db.Exec(migration1 + migration2 + migration3); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA application_id=%d;PRAGMA user_version=3", applicationID)); err != nil {
				t.Fatal(err)
			}
			id, workspace, session, agent := opaqueID(), opaqueID(), opaqueID(), opaqueID()
			credential := Credential{Kind: AgentCapability, Token: opaqueID()}
			timestamp := now()
			if _, err = db.Exec("INSERT INTO metadata VALUES('database_id',?);INSERT INTO workspaces VALUES(?,?,?);INSERT INTO sessions(id,workspace_id,name,created_at,updated_at) VALUES(?,?,?,?,?);INSERT INTO agents(id,workspace_id,session_id,name,created_at,updated_at) VALUES(?,?,?,?,?,?);UPDATE sessions SET orchestrator_id=? WHERE id=?;INSERT INTO capabilities VALUES(?,'agent',?,?)", id, workspace, privateTestDir(t), timestamp, session, workspace, "v3", timestamp, timestamp, agent, workspace, session, "v3-agent", timestamp, timestamp, agent, session, tokenHash(credential.Token), agent, session); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(expiresStandIn); err != nil {
				t.Fatal(err)
			}
			old := &Store{db: db, path: path, id: id}
			if err = old.ensureOperator(ctx, dir); err != nil {
				t.Fatal(err)
			}
			operator, err := ReadCredential(filepath.Join(dir, "operator.json"))
			if err != nil {
				t.Fatal(err)
			}
			ref := Context{workspace, session}
			// Today's Task code counts mail and writes the v6 archive columns. Stand-ins
			// let this v3 store write its Task and receipts; they are undone before the upgrade.
			if _, err = db.Exec("CREATE TABLE mail_messages(task_id TEXT,sequence INTEGER,received_at TEXT); CREATE TABLE task_claims(task_id TEXT,path TEXT); CREATE TABLE expired_operations(actor TEXT,operation_id TEXT,outcome TEXT);" + migration6); err != nil {
				t.Fatal(err)
			}
			request := TaskCreateRequest{Context: ref, OperationID: "v3-retained-task", Title: "Retained v3 Task", Description: "Keep this report-independent body"}
			task, err := old.CreateTask(ctx, credential.Token, request)
			if err != nil {
				t.Fatal(err)
			}
			_, err = old.ChangeTask(ctx, credential.Token, "assign", TaskChangeRequest{Context: ref, TaskID: task.ResourceID, OperationID: "v3-retained-assignment", ExpectedRevision: 1, AgentID: agent})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(undoMigration6 + dropExpires + "DROP TABLE mail_messages; DROP TABLE task_claims; DROP TABLE expired_operations"); err != nil {
				t.Fatal(err)
			}
			if fail {
				if _, err = db.Exec("CREATE TABLE mail_mentions(sentinel TEXT);INSERT INTO mail_mentions VALUES('preserve-v3')"); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			service, err := Start(ctx, dir, "")
			if fail {
				requireCode(t, err, "storage_unavailable")
				db, _ = sql.Open("sqlite3", path)
				var version, count int
				var sentinel string
				if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 3 || db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='mail_messages'").Scan(&count) != nil || count != 0 || db.QueryRow("SELECT sentinel FROM mail_mentions").Scan(&sentinel) != nil || sentinel != "preserve-v3" {
					t.Fatal("v4 failure damaged prior schema/data")
				}
				if _, err = db.Exec("DROP TABLE mail_mentions"); err != nil {
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
				t.Fatal("mail migration changed identity/version")
			}
			if p, err := authenticate(ctx, service.Store.db, operator.Token); err != nil || p.Kind != OperatorCapability {
				t.Fatal("migration changed Operator credential")
			}
			replay, err := service.Store.CreateTask(ctx, credential.Token, request)
			if err != nil || replay != task {
				t.Fatal("v3 receipt recovery changed")
			}
			detail, err := service.Store.ShowTask(ctx, credential.Token, TaskReadRequest{Context: ref, TaskID: task.ResourceID})
			if err != nil || detail.Description != request.Description || detail.AssigneeCount != 1 || detail.Revision != 2 {
				t.Fatal("v3 Task data/assignment lost")
			}
			sent, err := service.Store.SendMail(ctx, credential.Token, MailSendRequest{Context: ref, TaskID: task.ResourceID, OperationID: "upgraded-mail-send", Body: "Mail after migration"})
			if err != nil || sent.Revision != 1 {
				t.Fatal("new mail unusable", err)
			}
			var integrity string
			if service.Store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
				t.Fatal("v4 integrity")
			}
		})
	}
}
