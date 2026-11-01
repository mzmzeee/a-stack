package stack

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Both matrices start from this exact database, restored between cells so a
// successful mutation cannot change a later caller's or state's prerequisites.
type matrixFixture struct {
	taskTeam
	task, otherTask, question, reference, message, transfer string
	tokens                                                  map[string]string
	tables                                                  []string
	path                                                    string
	inviteCode, ownerCode, viewCode                         string
	importing                                               TransferImportRequest
}

var matrixCallers = []string{"none", "bad", "owner", "friend", "orchestrator", "worker", "foreign-agent", "foreign-operator", "browser", "revoked"}

func newMatrixFixture(t *testing.T) *matrixFixture {
	t.Helper()

	home := privateTestDir(t)
	for _, name := range []string{"HOME", "USERPROFILE", "AppData", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, home)
	}

	f := &matrixFixture{taskTeam: tasksFixture(t), tokens: map[string]string{}, path: privateTestDir(t)}
	f.tokens["owner"], f.tokens["orchestrator"], f.tokens["worker"] = f.operator.Token, f.orch.Token, f.a.Token
	f.tokens["bad"] = opaqueID()

	foreign, _ := f.register(t, 1, "foreign-agent")
	f.tokens["foreign-agent"] = foreign.Token
	for _, name := range []string{"friend", "foreign-operator", "browser", "revoked"} {
		secret := opaqueID()

		f.tokens[name] = secret
		kind, subject, session := "operator", opaqueID(), ""
		if name == "browser" {
			kind, session = "browser", f.sessions[0]
		}

		execTest(t, f.service.Store, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,?,?,?)", tokenHash(secret), kind, subject, session)

		if name == "friend" || name == "foreign-operator" {
			session = f.sessions[0]
			if name == "foreign-operator" {
				session = f.sessions[1]
			}

			execTest(t, f.service.Store, "INSERT INTO session_operators(token_hash,session_id,login) VALUES(?,?,?)", tokenHash(secret), session, name+"@example.com")
		}
	}

	execTest(t, f.service.Store, "UPDATE capabilities SET expires_at=? WHERE kind='browser'", clockNow().Unix()+3600)
	execTest(t, f.service.Store, "DELETE FROM capabilities WHERE token_hash=?", tokenHash(f.tokens["revoked"]))

	if _, err := f.service.Store.BindAgent(ctx, f.b.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 1, Runtime: &RuntimeBinding{InstanceID: "observe-runtime"}}); err != nil {
		t.Fatal(err)
	}

	f.task = f.create(t, "Matrix target").ResourceID
	f.otherTask = f.create(t, "Matrix prerequisite").ResourceID
	for _, id := range []string{f.orchID, f.aID} {
		f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.task, ExpectedRevision: f.show(t, f.task).Revision, AgentID: id})
	}

	f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: f.task, ExpectedRevision: f.show(t, f.task).Revision, Question: "Matrix question?"})
	f.question = f.show(t, f.task).Question.ID
	f.change(t, f.a.Token, "reference.add", TaskChangeRequest{TaskID: f.task, ExpectedRevision: f.show(t, f.task).Revision, Location: "matrix.txt"})

	if err := f.service.Store.db.QueryRow("SELECT id FROM task_references WHERE task_id=?", f.task).Scan(&f.reference); err != nil {
		t.Fatal(err)
	}

	m, err := f.service.Store.SendMail(ctx, f.orch.Token, MailSendRequest{Context: f.ref(0), TaskID: f.task, OperationID: opaqueID(), Body: "@02-worker matrix", AckRequired: true})
	if err != nil {
		t.Fatal(err)
	}

	f.message = m.ResourceID

	b, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/matrix"})
	if err != nil {
		t.Fatal(err)
	}

	f.transfer = b.TransferID
	f.inviteCode, f.ownerCode, f.viewCode = opaqueID(), opaqueID(), opaqueID()
	if _, err = f.service.Store.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "matrix@example.com", CodeHash: tokenHash(f.inviteCode)}); err != nil {
		t.Fatal(err)
	}

	if err = f.service.Store.CreateOwnerCode(ctx, f.operator.Token, tokenHash(f.ownerCode)); err != nil {
		t.Fatal(err)
	}

	if _, err = f.service.Store.OpenBrowser(ctx, f.operator.Token, BrowserOpenRequest{Context: f.ref(0), OperationID: opaqueID(), CredentialHash: tokenHash(f.viewCode)}); err != nil {
		t.Fatal(err)
	}

	// Export after invitation/view setup so seal has a matching read revision.
	b, err = f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/matrix"})
	if err != nil {
		t.Fatal(err)
	}

	f.transfer = b.TransferID

	source := tasksFixture(t)
	incoming, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/import"})
	if err != nil {
		t.Fatal(err)
	}

	f.importing = TransferImportRequest{Bundle: incoming, AgentHashes: transferHashes(t, source, 0)}
	if err = f.service.SetServeHost("matrix.example.ts.net"); err != nil {
		t.Fatal(err)
	}

	rows, err := f.service.Store.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}

	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}

		f.tables = append(f.tables, name)
	}

	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}

	rows.Close()
	// Mail integrity triggers need their parent message even with deferred FKs.
	slices.SortStableFunc(f.tables, func(a, b string) int {
		if a == "mail_messages" {
			return -1
		}

		if b == "mail_messages" {
			return 1
		}

		return strings.Compare(a, b)
	})

	for _, name := range f.tables {
		execTest(t, f.service.Store, `CREATE TEMP TABLE "matrix_`+name+`" AS SELECT * FROM "`+name+`"`)
	}

	return f
}

func (f *matrixFixture) reset(t *testing.T) {
	t.Helper()

	tx, err := f.service.Store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	if _, err = tx.Exec("PRAGMA defer_foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}

	for _, name := range f.tables {
		if _, err = tx.Exec(`DELETE FROM "` + name + `"`); err != nil {
			t.Fatal(name, err)
		}
	}

	for _, name := range f.tables {
		if _, err = tx.Exec(`INSERT INTO "` + name + `" SELECT * FROM "matrix_` + name + `"`); err != nil {
			t.Fatal(name, err)
		}
	}

	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type matrixBody struct {
	io.Reader
	reads int
}

func (b *matrixBody) Read(p []byte) (int, error) { b.reads++; return b.Reader.Read(p) }
func (*matrixBody) Close() error                 { return nil }

func (f *matrixFixture) call(t *testing.T, route, caller string) (string, int) {
	t.Helper()

	publicAsset := strings.HasPrefix(route, "/assets/") || strings.HasPrefix(route, "/view/")
	var body any
	if !publicAsset {
		body = f.request(t, route)
	}

	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}

	probe := &matrixBody{Reader: bytes.NewReader(data)}
	method := http.MethodPost
	if publicAsset || route == "/v1/credential" || route == "/v1/health" || route == "/v1/storage" {
		method = http.MethodGet
		probe.Reader = strings.NewReader("")
	}

	path := strings.NewReplacer("{workspace}", f.workspace, "{session}", f.sessions[0]).Replace(route)

	req := httptest.NewRequest(method, f.service.Discovery.URL+path, probe)
	if method == http.MethodGet {
		req.ContentLength = 0
		req.Body = http.NoBody
	}

	req.RemoteAddr = "127.0.0.1:12345"
	req.Host = strings.TrimPrefix(f.service.Discovery.URL, "http://")
	if route == "/v1/session/join" || route == "/v1/connect/join" {
		req.Host = "matrix.example.ts.net"
		req.Header.Set("Tailscale-User-Login", "matrix@example.com")
	}

	req.Header.Set("X-A-Stack-Instance", f.service.Discovery.InstanceID)
	req.Header.Set("Content-Type", "application/json")

	if route == "/v1/session/join" || route == "/v1/connect/join" {
		req.Header.Set("Origin", "https://matrix.example.ts.net")
	} else {
		req.Header.Set("Origin", f.service.Discovery.URL)
	}

	if caller != "none" {
		bearer := f.tokens[caller]
		// Observation uses the bound worker; attach uses the unbound worker.
		if route == "/v1/agent/observe" && caller == "worker" {
			bearer = f.b.Token
		}

		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	w := httptest.NewRecorder()
	f.service.dispatch(w, req)

	if publicAsset && w.Code == http.StatusOK {
		if w.Body.Len() == 0 || w.Header().Get("Content-Type") == "" {
			t.Fatal("empty browser asset")
		}

		return "allowed", probe.reads
	}

	var envelope struct {
		Error *Error `json:"error"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("%s: non-JSON response: %v", route, err)
	}

	if envelope.Error != nil {
		t.Log(envelope.Error.Message)

		statuses := map[string]int{"unauthorized": 401, "forbidden": 403, "not_found": 404, "conflict": 409, "session_closed": 409}
		if status, ok := statuses[envelope.Error.Code]; ok && w.Code != status {
			t.Errorf("%s returned HTTP %d, want %d", envelope.Error.Code, w.Code, status)
		}

		return envelope.Error.Code, probe.reads
	}

	if w.Code != http.StatusOK {
		t.Fatalf("%s: unexpected HTTP %d", route, w.Code)
	}

	return "allowed", probe.reads
}

func (f *matrixFixture) request(t *testing.T, route string) any {
	t.Helper()
	ref := f.ref(0)
	task := TaskChangeRequest{Context: ref, OperationID: opaqueID(), TaskID: f.task, ExpectedRevision: 6}
	// Fixture setup, not an assertion: every mutation uses the current revision.
	if err := f.service.Store.db.QueryRow("SELECT revision FROM tasks WHERE id=?", f.task).Scan(&task.ExpectedRevision); err != nil {
		t.Fatal(err)
	}

	action := strings.TrimPrefix(route, "/v1/task/")
	switch action {
	case "edit":
		task.Title = pointer("Changed matrix")
	case "assign":
		task.AgentID = f.bID
	case "unassign":
		task.AgentID = f.aID
	case "status":
		task.Progress = "working"
	case "reopen":
		task.Reason = "More work"
	case "report":
		task.Report = pointer("Matrix report")
	case "attention/set":
		task.Question = "Another question?"
	case "attention/resolve":
		task.Note = "Resolved"
	case "reference/add":
		task.Location = "another.txt"
	case "reference/remove":
		task.ReferenceID = f.reference
	}

	switch route {
	case "/v1/credential", "/v1/health", "/v1/storage", "/v1/session/links":
		return struct{}{}
	case "/v1/storage/backup":
		return map[string]string{"destination": filepath.Join(f.path, opaqueID()+".db")}
	case "/v1/workspace/create":
		return WorkspaceCreateRequest{OperationID: opaqueID(), Path: f.path}
	case "/v1/workspace/list":
		return ListRequest{Context: ref}
	case "/v1/session/list":
		return ListRequest{Context: ref}
	case "/v1/agent/list":
		return ListRequest{Context: ref}
	case "/v1/workspace/orders/show":
		return ref
	case "/v1/workspace/orders/set", "/v1/workspace/orders/clear":
		q := OrdersRequest{WorkspaceID: f.workspace, OperationID: opaqueID(), ExpectedRevision: 1}
		if route == "/v1/workspace/orders/set" {
			q.Body = "Matrix orders"
		}

		return q
	case "/v1/session/create":
		return SessionCreateRequest{WorkspaceID: f.workspace, OperationID: opaqueID(), Name: "Matrix new"}
	case "/v1/session/inspect":
		return ref
	case "/v1/session/close", "/v1/session/reopen", "/v1/session/appoint":
		q := SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: 2}
		if route == "/v1/session/appoint" {
			q.AgentID = f.bID
		}

		return q
	case "/v1/agent/register":
		return AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: "new-agent", CredentialHash: tokenHash(opaqueID())}
	case "/v1/agent/attach", "/v1/agent/rebind", "/v1/herdr/bind":
		q := AgentBindingRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: 1, AgentID: f.aID, Runtime: &RuntimeBinding{InstanceID: "matrix-runtime"}}
		if route == "/v1/agent/rebind" {
			q.CredentialHash = tokenHash(opaqueID())
		}

		return q
	case "/v1/agent/observe":
		return AgentObserveRequest{Context: ref, AgentID: f.bID, ExpectedRevision: 2, RuntimeID: "observe-runtime", State: "working"}
	case "/v1/task/create":
		return TaskCreateRequest{Context: ref, OperationID: opaqueID(), Title: "New task"}
	case "/v1/task/show", "/v1/task/claims":
		return TaskReadRequest{Context: ref, TaskID: f.task}
	case "/v1/task/claim", "/v1/task/unclaim":
		return ClaimRequest{Context: ref, TaskID: f.task, OperationID: opaqueID(), ExpectedRevision: task.ExpectedRevision, Paths: []string{"matrix.go"}}
	case "/v1/session/snapshot":
		return SnapshotRequest{Context: ref}
	case "/v1/session/page", "/v1/task/list", "/v1/task/history", "/v1/task/assignees", "/v1/task/references", "/v1/task/dependencies":
		q := SnapshotPageRequest{Context: ref, TaskID: f.task}
		if route == "/v1/session/page" || route == "/v1/task/list" {
			q.TaskID = ""
		}

		if route == "/v1/session/page" {
			q.Kind = "tasks"
		}

		return q
	case "/v1/task/answer":
		return AnswerRequest{Context: ref, TaskID: f.task, QuestionID: f.question, OperationID: opaqueID(), Answer: "Yes"}
	case "/v1/dependency/add", "/v1/dependency/remove":
		return DependencyRequest{Context: ref, OperationID: opaqueID(), ExpectedGraphRevision: 3, PrerequisiteID: f.otherTask, DependentID: f.task}
	case "/v1/mail/send":
		return MailSendRequest{Context: ref, TaskID: f.task, OperationID: opaqueID(), Body: "Matrix message"}
	case "/v1/mail/reply":
		return MailReplyRequest{Context: ref, MessageID: f.message, OperationID: opaqueID(), Body: "Matrix reply"}
	case "/v1/mail/ack":
		return MailAckRequest{Context: ref, MessageID: f.message, OperationID: opaqueID()}
	case "/v1/mail/read":
		return MailReadRequest{Context: ref, TaskID: f.task}
	case "/v1/mail/recipients":
		return MailRecipientsRequest{Context: ref, TaskID: f.task}
	case "/v1/mail/check":
		return MailCheckRequest{Context: ref}
	case "/v1/mail/urgent":
		return UrgentMailRequest{Context: ref}
	case "/v1/relay/claim", "/v1/relay/begin", "/v1/relay/result":
		return RelayRequest{Context: ref, Generation: 1, RuntimeID: "unbound", Proof: opaqueID()}
	case "/v1/notifications/status", "/v1/notifications/drain":
		return NoticeRequest{Context: ref}
	case "/v1/herdr/focus":
		return map[string]any{"workspace_id": f.workspace, "session_id": f.sessions[0], "agent_id": f.aID}
	case "/v1/browser/open":
		return BrowserOpenRequest{Context: ref, OperationID: opaqueID(), CredentialHash: tokenHash(opaqueID())}
	case "/v1/browser/revoke":
		return ViewRevokeRequest{Context: ref}
	case "/v1/browser/redeem":
		return ViewRedeemRequest{Context: ref, Code: f.viewCode}
	case "/v1/connect/code":
		return map[string]string{"code_hash": tokenHash(opaqueID())}
	case "/v1/connect/join", "/v1/session/join":
		code, session := f.inviteCode, f.sessions[0]
		if route == "/v1/connect/join" {
			code, session = f.ownerCode, ""
		}

		return SessionJoinRequest{SessionID: session, Code: code, CredentialHash: tokenHash(opaqueID())}
	case "/v1/session/invite":
		return SessionInviteRequest{Context: ref, OperationID: opaqueID(), Login: "new@example.com", CodeHash: tokenHash(opaqueID())}
	case "/v1/session/link":
		return SessionLinkRequest{Link: SessionLink{Context: ref, Path: f.path, Remote: "matrix", DatabaseID: opaqueID(), CredentialFile: filepath.Join(f.path, "friend.json"), Name: "Linked"}}
	case "/v1/session/transfer/export":
		return TransferExportRequest{Context: ref, TransferID: opaqueID(), Repository: "example.com/matrix"}
	case "/v1/session/transfer/import":
		return f.importing
	case "/v1/session/transfer/status", "/v1/session/transfer/seal", "/v1/session/transfer/activate", "/v1/session/transfer/recover":
		return TransferChangeRequest{Context: ref, TransferID: f.transfer, DestinationDatabase: opaqueID(), DestinationStopped: true}
	}

	if strings.HasPrefix(route, "/v1/task/") {
		return task
	}

	t.Fatalf("missing request fixture: %s", route)

	return nil
}

// Walk production syntax, not a second router list. Service dispatch helpers
// contain the concrete route literals; client-only strings are deliberately excluded.
func matrixRoutes(t *testing.T) []string {
	t.Helper()

	routes, err := matrixParseRoutes(matrixSourceFiles(t, "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	return routes
}
