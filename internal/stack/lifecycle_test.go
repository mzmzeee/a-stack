package stack

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type identityFixture struct {
	service   *Service
	dir       string
	operator  Credential
	workspace string
	sessions  [2]string
}

func identities(t *testing.T) identityFixture {
	t.Helper()
	s, dir, c := startTest(t)
	f := identityFixture{service: s, dir: dir, operator: c}
	w, err := s.Store.CreateWorkspace(ctx, c.Token, WorkspaceCreateRequest{"workspace-create", privateTestDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	f.workspace = w.ResourceID
	for i := range f.sessions {
		r, err := s.Store.CreateSession(ctx, c.Token, SessionCreateRequest{fmt.Sprintf("session-create-%d", i), w.ResourceID, fmt.Sprintf("Session %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		f.sessions[i] = r.ResourceID
	}
	return f
}
func (f identityFixture) ref(i int) Context { return Context{f.workspace, f.sessions[i]} }
func (f identityFixture) register(t *testing.T, i int, name string) (Credential, Receipt) {
	t.Helper()
	c := Credential{Kind: AgentCapability, Token: opaqueID()}
	r, err := f.service.Store.RegisterAgent(ctx, f.operator.Token, AgentRegisterRequest{Context: f.ref(i), OperationID: "register-" + name, Name: name, CredentialHash: tokenHash(c.Token)})
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}
func (f identityFixture) appoint(t *testing.T, i int, id string, revision int64) Receipt {
	t.Helper()
	r, err := f.service.Store.ChangeSession(ctx, f.operator.Token, "appoint", SessionChangeRequest{Context: f.ref(i), OperationID: "appoint-" + opaqueID(), ExpectedRevision: revision, AgentID: id})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestLifecycleContextsRolesAndReplacement(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	bootstrap, err := s.InspectSession(ctx, f.operator.Token, f.ref(0))
	if err != nil || bootstrap.OrchestratorID != nil || bootstrap.Revision != 1 {
		t.Fatal("bootstrap holder must be explicitly absent")
	}
	orch, a := f.register(t, 0, "01-orchestrator")
	worker, b := f.register(t, 0, "02-worker")
	other, _ := f.register(t, 1, "03-other")
	req := AgentRegisterRequest{Context: f.ref(0), OperationID: "before-appointment", Name: "new-worker", CredentialHash: tokenHash(opaqueID())}
	_, err = s.RegisterAgent(ctx, orch.Token, req)
	requireCode(t, err, "forbidden")
	f.appoint(t, 0, a.ResourceID, 1)
	req.OperationID = "after-appointment"
	registered, err := s.RegisterAgent(ctx, orch.Token, req)
	if err != nil {
		t.Fatal("Orchestrator without runtime lost registration authority", err)
	}
	retried, err := s.RegisterAgent(ctx, orch.Token, req)
	if err != nil || retried != registered {
		t.Fatal("registration replay")
	}
	for _, token := range []string{orch.Token, worker.Token, other.Token} {
		i := 0
		if token == other.Token {
			i = 1
		}
		if _, err = s.InspectSession(ctx, token, f.ref(i)); err != nil {
			t.Fatal("own Session read", err)
		}
		_, err = s.InspectSession(ctx, token, f.ref(1-i))
		requireCode(t, err, "forbidden")
		_, err = s.InspectSession(ctx, token, Context{SessionID: f.sessions[i]})
		requireCode(t, err, "invalid_input")
		_, err = s.ListLifecycle(ctx, token, "session.list", ListRequest{Context: Context{WorkspaceID: f.workspace}})
		requireCode(t, err, "invalid_input")
	}
	_, err = s.RegisterAgent(ctx, other.Token, req)
	requireCode(t, err, "forbidden")
	_, err = s.CreateSession(ctx, worker.Token, SessionCreateRequest{"worker-session-create", f.workspace, "forbidden"})
	requireCode(t, err, "forbidden")
	_, err = s.ChangeSession(ctx, worker.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: "worker-close", ExpectedRevision: 2})
	requireCode(t, err, "forbidden")
	f.appoint(t, 0, b.ResourceID, 2)
	req.OperationID = "old-holder-register"
	req.Name = "old-holder-new"
	_, err = s.RegisterAgent(ctx, orch.Token, req)
	requireCode(t, err, "forbidden")
	// The former holder retains its registered identity and ordinary Agent access.
	_, err = s.BindAgent(ctx, orch.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: "old-holder-attach", AgentID: a.ResourceID, ExpectedRevision: 1, Runtime: &RuntimeBinding{InstanceID: "old-holder-instance"}})
	if err != nil {
		t.Fatal("replacement removed ordinary Agent authority", err)
	}
	req.OperationID = "new-holder-register"
	req.CredentialHash = tokenHash(opaqueID())
	_, err = s.RegisterAgent(ctx, worker.Token, req)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ChangeSession(ctx, f.operator.Token, "appoint", SessionChangeRequest{Context: f.ref(0), OperationID: "stale-appointment", AgentID: a.ResourceID, ExpectedRevision: 2})
	requireCode(t, err, "conflict")
	if err.(*Error).CurrentRevision == nil || *err.(*Error).CurrentRevision != 3 {
		t.Fatal("missing current revision")
	}
	// Names are case sensitive and Workspace-unique even across Sessions.
	_, err = s.RegisterAgent(ctx, f.operator.Token, AgentRegisterRequest{Context: f.ref(1), OperationID: "duplicate-name", Name: "02-worker", CredentialHash: tokenHash(opaqueID())})
	requireCode(t, err, "conflict")
	_, err = s.RegisterAgent(ctx, f.operator.Token, AgentRegisterRequest{Context: f.ref(1), OperationID: "different-case", Name: "02-Worker", CredentialHash: tokenHash(opaqueID())})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bad name", "@worker", "-worker", string(bytes.Repeat([]byte("a"), 65))} {
		_, err = s.RegisterAgent(ctx, f.operator.Token, AgentRegisterRequest{Context: f.ref(1), OperationID: "invalid-name-" + opaqueID(), Name: name, CredentialHash: tokenHash(opaqueID())})
		requireCode(t, err, "invalid_input")
	}
}
func TestCompetingAppointmentsAndClosureLinearization(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	_, a := f.register(t, 0, "first")
	_, b := f.register(t, 0, "second")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{a.ResourceID, b.ResourceID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := s.ChangeSession(ctx, f.operator.Token, "appoint", SessionChangeRequest{Context: f.ref(0), OperationID: "compete-" + id, AgentID: id, ExpectedRevision: 1})
			results <- err
		}(id)
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			requireCode(t, err, "conflict")
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("competing holders did not serialize")
	}
	snapshot, err := s.InspectSession(ctx, f.operator.Token, f.ref(0))
	if err != nil || snapshot.OrchestratorID == nil || snapshot.Revision != 2 {
		t.Fatal("one designated holder required")
	}
	// Register and close race on the exact production guard/transaction path.
	closure := SessionChangeRequest{Context: f.ref(0), OperationID: "racing-close", ExpectedRevision: 2}
	registration := AgentRegisterRequest{Context: f.ref(0), OperationID: "racing-register", Name: "racing-worker", CredentialHash: tokenHash(opaqueID())}
	start := make(chan struct{})
	regErr := make(chan error, 1)
	closeErr := make(chan error, 1)
	go func() { <-start; _, err := s.RegisterAgent(ctx, f.operator.Token, registration); regErr <- err }()
	go func() { <-start; _, err := s.ChangeSession(ctx, f.operator.Token, "close", closure); closeErr <- err }()
	close(start)
	err = <-regErr
	if err != nil {
		requireCode(t, err, "session_closed")
	}
	if err = <-closeErr; err != nil {
		t.Fatal(err)
	}
	var afterClose int
	if s.db.QueryRow(`SELECT count(*) FROM lifecycle_history h WHERE h.action='agent.register' AND h.sequence>(SELECT sequence FROM lifecycle_history WHERE resource_id=? AND action='session.close')`, f.sessions[0]).Scan(&afterClose) != nil || afterClose != 0 {
		t.Fatal("registration committed after closure")
	}
	_, err = s.RegisterAgent(ctx, f.operator.Token, AgentRegisterRequest{Context: f.ref(0), OperationID: "post-close-register", Name: "after-close", CredentialHash: tokenHash(opaqueID())})
	requireCode(t, err, "session_closed")
	receipt, err := s.ChangeSession(ctx, f.operator.Token, "close", closure)
	if err != nil {
		t.Fatal("closed receipt replay", err)
	}
	replay, err := s.ChangeSession(ctx, f.operator.Token, "close", closure)
	if err != nil || receipt != replay {
		t.Fatal("close replay changed")
	}
	reopened, err := s.ChangeSession(ctx, f.operator.Token, "reopen", SessionChangeRequest{Context: f.ref(0), OperationID: "reopen-session", ExpectedRevision: 3})
	if err != nil || reopened.Revision != 4 {
		t.Fatal("reopen revision", err)
	}
	if countTest(t, s, "SELECT count(*) FROM agents") < 2 {
		t.Fatal("closure discarded history")
	}
}
func TestRuntimeClaimsRebindAndClosedObservation(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	first, a := f.register(t, 0, "first-agent")
	second, b := f.register(t, 1, "second-agent")
	binding := &RuntimeBinding{InstanceID: "runtime-first", Endpoint: "private-endpoint", PaneID: "pane-1", TerminalID: "term-1", NativeSessionID: "native-1", HerdrAlias: "private-alias", Harness: "codex", HarnessVersion: "unverified"}
	attached, err := s.BindAgent(ctx, first.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: "attach-first", AgentID: a.ResourceID, ExpectedRevision: 1, Runtime: binding})
	if err != nil || attached.Revision != 2 {
		t.Fatal(err)
	}
	renamed := *binding
	renamed.InstanceID = "runtime-second"
	renamed.HerdrAlias = "changed-alias"
	_, err = s.BindAgent(ctx, second.Token, "attach", AgentBindingRequest{Context: f.ref(1), OperationID: "attach-conflict", AgentID: b.ResourceID, ExpectedRevision: 1, Runtime: &renamed})
	requireCode(t, err, "conflict")
	// A subset of supplied stable identity still reserves the existing target.
	for i, partial := range []*RuntimeBinding{{InstanceID: "partial-pane", Endpoint: binding.Endpoint, PaneID: binding.PaneID}, {InstanceID: "partial-terminal", Endpoint: binding.Endpoint, TerminalID: binding.TerminalID}, {InstanceID: "partial-native", NativeSessionID: binding.NativeSessionID}, {InstanceID: binding.InstanceID}} {
		_, err = s.BindAgent(ctx, second.Token, "attach", AgentBindingRequest{Context: f.ref(1), OperationID: fmt.Sprintf("partial-attach-%d", i), AgentID: b.ResourceID, ExpectedRevision: 1, Runtime: partial})
		requireCode(t, err, "conflict")
	}
	_, err = s.BindAgent(ctx, first.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: "silent-replace", AgentID: a.ResourceID, ExpectedRevision: 2, Runtime: &RuntimeBinding{InstanceID: "new-id"}})
	requireCode(t, err, "conflict")
	_, err = s.BindAgent(ctx, first.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: "worker-rebind", AgentID: a.ResourceID, ExpectedRevision: 2, CredentialHash: tokenHash(opaqueID())})
	requireCode(t, err, "forbidden")
	// Failed conflicting rebind must preserve the original credential and claims.
	own, err := s.BindAgent(ctx, second.Token, "attach", AgentBindingRequest{Context: f.ref(1), OperationID: "attach-independent", AgentID: b.ResourceID, ExpectedRevision: 1, Runtime: &RuntimeBinding{InstanceID: "independent"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(1), OperationID: "conflicting-rebind", AgentID: b.ResourceID, ExpectedRevision: own.Revision, Runtime: &renamed, CredentialHash: tokenHash(opaqueID())})
	requireCode(t, err, "conflict")
	if _, err = authenticate(ctx, s.db, second.Token); err != nil {
		t.Fatal("failed rebind revoked old credential")
	}
	newCredential := Credential{Kind: AgentCapability, Token: opaqueID()}
	rebindRequest := AgentBindingRequest{Context: f.ref(0), OperationID: "operator-clear-runtime", AgentID: a.ResourceID, ExpectedRevision: 2, CredentialHash: tokenHash(newCredential.Token)}
	rebound, err := s.BindAgent(ctx, f.operator.Token, "rebind", rebindRequest)
	if err != nil || rebound.ResourceID != a.ResourceID || rebound.Revision != 3 {
		t.Fatal(err)
	}
	replay, err := s.BindAgent(ctx, f.operator.Token, "rebind", rebindRequest)
	if err != nil || replay != rebound {
		t.Fatal("retry rotated credential or rebound twice")
	}
	_, err = authenticate(ctx, s.db, first.Token)
	requireCode(t, err, "unauthorized")
	if _, err = authenticate(ctx, s.db, newCredential.Token); err != nil {
		t.Fatal("new credential missing")
	}
	// Explicit Operator clearing released the old target for the other Session.
	newSecond := Credential{Kind: AgentCapability, Token: opaqueID()}
	_, err = s.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(1), OperationID: "rebind-released-target", AgentID: b.ResourceID, ExpectedRevision: 2, Runtime: &renamed, CredentialHash: tokenHash(newSecond.Token)})
	if err != nil {
		t.Fatal("released target unavailable", err)
	}
	f.appoint(t, 1, b.ResourceID, 1)
	_, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(1), OperationID: "close-for-observation", ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := s.ObserveAgent(ctx, newSecond.Token, AgentObserveRequest{Context: f.ref(1), AgentID: b.ResourceID, ExpectedRevision: 3, RuntimeID: renamed.InstanceID, State: "done"})
	if err != nil || observed.ObservedAt == "" || observed.Revision != 3 {
		t.Fatal("closed observation", err)
	}
	closed, err := s.InspectSession(ctx, newSecond.Token, f.ref(1))
	if err != nil || !closed.Closed || closed.Revision != 3 || closed.OrchestratorID == nil || *closed.OrchestratorID != b.ResourceID {
		t.Fatal("observation changed Session lifecycle/role")
	}
	// Metadata exposes no private binding, token hashes or enrollment secrets.
	page, err := s.ListLifecycle(ctx, newSecond.Token, "agent.list", ListRequest{Context: f.ref(1)})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(page)
	for _, secret := range []string{binding.Endpoint, binding.PaneID, binding.TerminalID, binding.NativeSessionID, binding.HerdrAlias, renamed.InstanceID, newSecond.Token, tokenHash(newSecond.Token)} {
		if bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("private binding/credential leaked in metadata")
		}
	}
	if countTest(t, s, "SELECT count(*) FROM lifecycle_history WHERE resource_id='"+a.ResourceID+"'") < 3 {
		t.Fatal("original identity history lost")
	}
}
func TestClientEnrollmentLostResponseAndRestart(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	request := AgentRegisterRequest{Context: f.ref(0), OperationID: "durable-enrollment", Name: "enrolled-worker"}
	path := filepath.Join(privateTestDir(t), "worker.json")
	hash, err := PrepareAgentCredential(path, s.id, "agent register", request.OperationID, request)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	request.CredentialHash = hash
	first, err := s.RegisterAgent(ctx, f.operator.Token, request)
	if err != nil {
		t.Fatal(err)
	}
	request.CredentialHash = ""
	again, err := PrepareAgentCredential(path, s.id, "agent register", request.OperationID, request)
	if err != nil || again != hash {
		t.Fatal("lost-response enrollment changed token")
	}
	changed := request
	changed.Name = "changed-worker"
	_, err = PrepareAgentCredential(path, s.id, "agent register", request.OperationID, changed)
	requireCode(t, err, "operation_conflict")
	if err = f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, err := Start(ctx, f.dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	request.CredentialHash = hash
	replayed, err := restarted.Store.RegisterAgent(ctx, f.operator.Token, request)
	if err != nil || replayed != first {
		t.Fatal("restart enrollment duplicated or lost receipt")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("credential file changed")
	}
	credential, err := ReadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Store.InspectSession(ctx, credential.Token, f.ref(0)); err != nil {
		t.Fatal("delivered credential unusable", err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(restarted.Store.path + suffix)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(credential.Token)) {
			t.Fatal("plaintext token persisted server-side")
		}
	}
	changedContext := request
	changedContext.Context = f.ref(1)
	_, err = restarted.Store.RegisterAgent(ctx, f.operator.Token, changedContext)
	requireCode(t, err, "operation_conflict")
	if countTest(t, restarted.Store, "SELECT count(*) FROM agents") != 1 {
		t.Fatal("duplicate registration")
	}
}
func TestPopulatedV1UpgradeAndMigrationRollback(t *testing.T) {
	for _, failMigration := range []bool{false, true} {
		t.Run(fmt.Sprint(failMigration), func(t *testing.T) {
			dir := privateTestDir(t)
			path, err := ensureDatabaseFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			id := opaqueID()
			credential := Credential{Kind: OperatorCapability, Token: opaqueID()}
			if _, err = db.Exec(migration1); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA application_id=%d; PRAGMA user_version=1", applicationID)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("INSERT INTO metadata VALUES('database_id',?); INSERT INTO capabilities VALUES(?,'operator','original-operator','')", id, tokenHash(credential.Token)); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("INSERT INTO operation_receipts VALUES('operator:original-operator','v1-operation','v1','local','digest','retained',1,'created','','original-time')"); err != nil {
				t.Fatal(err)
			}
			if failMigration {
				if _, err = db.Exec("CREATE TABLE sessions(sentinel TEXT); INSERT INTO sessions VALUES('preserve')"); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			if err = writePrivate(filepath.Join(dir, "operator.json"), credential, false); err != nil {
				t.Fatal(err)
			}
			service, err := Start(ctx, dir, "")
			if failMigration {
				requireCode(t, err, "storage_unavailable")
				db, err = sql.Open("sqlite3", path)
				if err != nil {
					t.Fatal(err)
				}
				var version, partial int
				var sentinel string
				if db.QueryRow("PRAGMA user_version").Scan(&version) != nil || version != 1 || db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='workspaces'").Scan(&partial) != nil || partial != 0 || db.QueryRow("SELECT sentinel FROM sessions").Scan(&sentinel) != nil || sentinel != "preserve" {
					t.Fatal("v2 migration failure damaged v1")
				}
				if _, err = db.Exec("DROP TABLE sessions"); err != nil {
					t.Fatal(err)
				}
				db.Close()
				service, err = Start(ctx, dir, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close(ctx)
			if service.Store.id != id || countTest(t, service.Store, "PRAGMA user_version") != schemaVersion || countTest(t, service.Store, "SELECT count(*) FROM operation_receipts WHERE operation_id='v1-operation'") != 1 {
				t.Fatal("v1 identity/receipt lost")
			}
			if _, err = service.Store.CreateWorkspace(ctx, credential.Token, WorkspaceCreateRequest{"upgraded-workspace", privateTestDir(t)}); err != nil {
				t.Fatal("upgraded lifecycle unusable", err)
			}
			var integrity string
			if service.Store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
				t.Fatal("upgraded integrity")
			}
		})
	}
}
func TestLifecycleHTTPParsingAndBoundedScopedLists(t *testing.T) {
	f := identities(t)
	agent, _ := f.register(t, 0, "reader")
	for _, payload := range []string{`{"workspace_id":"x","session_id":"y","role":"operator"}`, `{"workspace_id":"x","session_id":"y","name":"fake","token":"secret"}`} {
		status, data := rawRequest(t, f.service, agent, "POST", "/v1/agent/register", bytes.NewBufferString(payload), map[string]string{"Content-Type": "application/json"})
		if status != 400 || !bytes.Contains(data, []byte("invalid_input")) {
			t.Fatal("body-supplied identity/role accepted")
		}
	}
	client, err := NewClient(f.dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	firstData, err := client.Do(ctx, "POST", "/v1/session/list", ListRequest{Context: Context{WorkspaceID: f.workspace}, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	var first struct {
		Data Page[Session] `json:"data"`
	}
	if json.Unmarshal(firstData, &first) != nil || len(first.Data.Items) != 1 || first.Data.Next == "" {
		t.Fatal("bounded list first page")
	}
	secondData, err := client.Do(ctx, "POST", "/v1/session/list", ListRequest{Context: Context{WorkspaceID: f.workspace}, Limit: 1, After: first.Data.Next})
	if err != nil {
		t.Fatal(err)
	}
	var second struct {
		Data Page[Session] `json:"data"`
	}
	if json.Unmarshal(secondData, &second) != nil || len(second.Data.Items) != 1 || second.Data.Next != "" || second.Data.Items[0].ID == first.Data.Items[0].ID {
		t.Fatal("list continuation duplicated or omitted data")
	}
	_, err = f.service.Store.ListLifecycle(ctx, agent.Token, "agent.list", ListRequest{Context: f.ref(1)})
	requireCode(t, err, "forbidden")
	_, err = f.service.Store.ListLifecycle(ctx, f.operator.Token, "session.list", ListRequest{Context: Context{WorkspaceID: f.workspace}, Limit: 101})
	requireCode(t, err, "invalid_input")
	_, err = f.service.Store.InspectSession(ctx, f.operator.Token, Context{WorkspaceID: opaqueID(), SessionID: f.sessions[0]})
	requireCode(t, err, "invalid_input")
}

func TestCommittedReplayAfterProjectOrWorktreeDisappears(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	project := filepath.Join(privateTestDir(t), "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	workspaceRequest := WorkspaceCreateRequest{OperationID: "disappearing-project", Path: project}
	workspace, err := s.CreateWorkspace(ctx, f.operator.Token, workspaceRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(project); err != nil {
		t.Fatal(err)
	}
	replay, err := s.CreateWorkspace(ctx, f.operator.Token, workspaceRequest)
	if err != nil || replay != workspace {
		t.Fatal("committed Workspace retry required vanished directory", err)
	}
	worktree := filepath.Join(privateTestDir(t), "worktree")
	if err = os.Mkdir(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	registration := AgentRegisterRequest{Context: f.ref(0), OperationID: "disappearing-worktree", Name: "retained-worktree-agent", WorktreePath: worktree, CredentialHash: tokenHash(opaqueID())}
	agent, err := s.RegisterAgent(ctx, f.operator.Token, registration)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(worktree); err != nil {
		t.Fatal(err)
	}
	replay, err = s.RegisterAgent(ctx, f.operator.Token, registration)
	if err != nil || replay != agent {
		t.Fatal("committed Agent retry required vanished directory", err)
	}
	fresh := registration
	fresh.OperationID = "new-missing-directory"
	fresh.Name = "new-agent"
	_, err = s.RegisterAgent(ctx, f.operator.Token, fresh)
	requireCode(t, err, "invalid_input")
	_, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: "close-disappeared-worktree", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	replay, err = s.RegisterAgent(ctx, f.operator.Token, registration)
	if err != nil || replay != agent {
		t.Fatal("closed committed replay used filesystem/write guard", err)
	}
	fresh.OperationID = "closed-missing-directory"
	_, err = s.RegisterAgent(ctx, f.operator.Token, fresh)
	requireCode(t, err, "session_closed")
	if countTest(t, s, "SELECT count(*) FROM agents") != 1 {
		t.Fatal("disappearance replay duplicated registration")
	}
}

func TestSessionReplayAuthorizesOriginalResourceContext(t *testing.T) {
	f := identities(t)
	s := f.service.Store
	other, err := s.CreateWorkspace(ctx, f.operator.Token, WorkspaceCreateRequest{OperationID: "other-workspace", Path: privateTestDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	changed := SessionCreateRequest{OperationID: "session-create-0", WorkspaceID: other.ResourceID, Name: "Session 0"}
	_, err = s.CreateSession(ctx, f.operator.Token, changed)
	requireCode(t, err, "operation_conflict")
	closeRequest := SessionChangeRequest{Context: f.ref(0), OperationID: "original-resource-close", ExpectedRevision: 1}
	original, err := s.ChangeSession(ctx, f.operator.Token, "close", closeRequest)
	if err != nil {
		t.Fatal(err)
	}
	closeRequest.Context = f.ref(1)
	_, err = s.ChangeSession(ctx, f.operator.Token, "close", closeRequest)
	requireCode(t, err, "operation_conflict")
	closeRequest.Context = f.ref(0)
	replay, err := s.ChangeSession(ctx, f.operator.Token, "close", closeRequest)
	if err != nil || replay != original {
		t.Fatal("original Session receipt unavailable", err)
	}
}
