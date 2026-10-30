package stack

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func lifecycleReviewSession(t *testing.T, s *Store, operator Credential, workspace string) Context {
	t.Helper()

	if workspace == "" {
		r, err := s.CreateWorkspace(context.Background(), operator.Token, WorkspaceCreateRequest{OperationID: opaqueID(), Path: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}

		workspace = r.ResourceID
	}

	r, err := s.CreateSession(context.Background(), operator.Token, SessionCreateRequest{OperationID: opaqueID(), WorkspaceID: workspace, Name: "review"})
	if err != nil {
		t.Fatal(err)
	}

	return Context{WorkspaceID: workspace, SessionID: r.ResourceID}
}

func lifecycleReviewAgent(t *testing.T, s *Store, issuer Credential, ref Context, name string) (Receipt, Credential) {
	t.Helper()
	c := Credential{Kind: AgentCapability, Token: opaqueID()}

	r, err := s.RegisterAgent(context.Background(), issuer.Token, AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: name, CredentialHash: tokenHash(c.Token)})
	if err != nil {
		t.Fatal(err)
	}

	return r, c
}

func TestLifecycleSpecAuthorityContextAndReplacement(t *testing.T) {
	ctx := context.Background()
	service, operator := reviewService(t, privateTestDir(t))
	s := service.Store
	ref := lifecycleReviewSession(t, s, operator, "")
	other := lifecycleReviewSession(t, s, operator, ref.WorkspaceID)

	foreign := lifecycleReviewSession(t, s, operator, "")
	initial, err := s.InspectSession(ctx, operator.Token, ref)
	if err != nil || initial.OrchestratorID != nil || initial.Revision != 1 {
		t.Fatal("bootstrap must have no designated Orchestrator")
	}

	a, ca := lifecycleReviewAgent(t, s, operator, ref, "01-orchestrator")
	b, cb := lifecycleReviewAgent(t, s, operator, ref, "worker")
	c, _ := lifecycleReviewAgent(t, s, operator, ref, "replacement-candidate")
	lifecycleReviewAgent(t, s, operator, other, "other-worker")
	appoint := SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: 1, AgentID: a.ResourceID}

	first, err := s.ChangeSession(ctx, operator.Token, "appoint", appoint)
	if err != nil {
		t.Fatal(err)
	}

	retry, err := s.ChangeSession(ctx, operator.Token, "appoint", appoint)
	if err != nil || retry != first {
		t.Fatal("appointment retry changed its receipt")
	}

	lifecycleReviewAgent(t, s, ca, ref, "registered-by-orchestrator")
	_, err = s.RegisterAgent(ctx, cb.Token, AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: "forbidden-worker", CredentialHash: tokenHash(opaqueID())})
	reviewCode(t, err, "forbidden")
	_, err = s.InspectSession(ctx, cb.Token, other)
	reviewCode(t, err, "forbidden")
	_, err = s.InspectSession(ctx, cb.Token, Context{WorkspaceID: foreign.WorkspaceID, SessionID: ref.SessionID})
	reviewCode(t, err, "invalid_input")
	_, err = s.ListLifecycle(ctx, cb.Token, "agent.list", ListRequest{Context: other})
	reviewCode(t, err, "forbidden")

	listed, err := s.ListLifecycle(ctx, cb.Token, "session.list", ListRequest{Context: ref})
	if err != nil || len(listed.(Page[Session]).Items) != 1 || listed.(Page[Session]).Items[0].ID != ref.SessionID {
		t.Fatal("Session list did not stay scoped")
	}

	_, err = s.ChangeSession(ctx, cb.Token, "close", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: first.Revision})
	reviewCode(t, err, "forbidden")

	replaced, err := s.ChangeSession(ctx, operator.Token, "appoint", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: first.Revision, AgentID: b.ResourceID})
	if err != nil {
		t.Fatal(err)
	}

	_, err = s.RegisterAgent(ctx, ca.Token, AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: "old-role-cannot-register", CredentialHash: tokenHash(opaqueID())})
	reviewCode(t, err, "forbidden")

	if _, err := s.InspectSession(ctx, ca.Token, ref); err != nil {
		t.Fatal("role replacement removed ordinary read access")
	}

	// No runtime is attached: ordinary registration authority still works.
	lifecycleReviewAgent(t, s, cb, ref, "new-role-still-works")

	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, id := range []string{a.ResourceID, c.ResourceID} {
		wg.Go(func() {
			_, err := s.ChangeSession(ctx, operator.Token, "appoint", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: replaced.Revision, AgentID: id})
			errors <- err
		})
	}

	wg.Wait()
	close(errors)

	var success, conflicts int
	for err := range errors {
		if err == nil {
			success++
		} else {
			reviewCode(t, err, "conflict")
			conflicts++
		}
	}

	if success != 1 || conflicts != 1 {
		t.Fatal("competing replacements did not produce exactly one winner")
	}

	current, err := s.InspectSession(ctx, operator.Token, ref)
	if err != nil || current.OrchestratorID == nil || current.Revision != replaced.Revision+1 {
		t.Fatal("appointment lost role holder")
	}
}

func TestLifecycleSpecClosureRaceAndReplay(t *testing.T) {
	ctx := context.Background()
	service, operator := reviewService(t, privateTestDir(t))
	s := service.Store

	ref := lifecycleReviewSession(t, s, operator, "")
	_, worker := lifecycleReviewAgent(t, s, operator, ref, "worker")
	for i := range 4 {
		before, err := s.InspectSession(ctx, operator.Token, ref)
		if err != nil {
			t.Fatal(err)
		}

		closeRequest := SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: before.Revision}
		registerRequest := AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: "race-" + opaqueID(), CredentialHash: tokenHash(opaqueID())}
		ready := make(chan struct{})
		type result struct {
			receipt Receipt
			err     error
		}
		registered := make(chan result, 1)
		closed := make(chan result, 1)
		go func() {
			<-ready
			r, e := s.RegisterAgent(ctx, operator.Token, registerRequest)
			registered <- result{r, e}
		}()
		go func() {
			<-ready
			r, e := s.ChangeSession(ctx, operator.Token, "close", closeRequest)
			closed <- result{r, e}
		}()
		close(ready)

		registration, closure := <-registered, <-closed
		if closure.err != nil {
			t.Fatal(closure.err)
		}

		var receipts int
		if err := s.db.QueryRow("SELECT count(*) FROM operation_receipts WHERE operation_id=?", registerRequest.OperationID).Scan(&receipts); err != nil {
			t.Fatal(err)
		}

		if registration.err == nil {
			var registrationSequence, closureSequence int
			if err := s.db.QueryRow("SELECT sequence FROM lifecycle_history WHERE action='agent.register' AND resource_id=?", registration.receipt.ResourceID).Scan(&registrationSequence); err != nil {
				t.Fatal(err)
			}

			if err := s.db.QueryRow("SELECT max(sequence) FROM lifecycle_history WHERE action='session.close' AND resource_id=?", ref.SessionID).Scan(&closureSequence); err != nil {
				t.Fatal(err)
			}

			if receipts != 1 || registrationSequence >= closureSequence {
				t.Fatal("write committed after closure")
			}
		} else {
			reviewCode(t, registration.err, "session_closed")

			if receipts != 0 {
				t.Fatal("rejected write persisted a receipt")
			}
		}

		replayed, err := s.ChangeSession(ctx, operator.Token, "close", closeRequest)
		if err != nil || replayed != closure.receipt {
			t.Fatal("closure retry changed receipt")
		}

		if _, err := s.InspectSession(ctx, worker.Token, ref); err != nil {
			t.Fatal("closed Session lost authorized read")
		}

		_, err = s.RegisterAgent(ctx, operator.Token, AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: "after-close", CredentialHash: tokenHash(opaqueID())})
		reviewCode(t, err, "session_closed")
		_, err = s.ChangeSession(ctx, operator.Token, "reopen", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: before.Revision})
		reviewCode(t, err, "conflict")

		_, err = s.ChangeSession(ctx, operator.Token, "reopen", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: closure.receipt.Revision})
		if err != nil {
			t.Fatal(err)
		}

		t.Logf("closure race %d linearized", i+1)
	}
}

func TestLifecycleSpecBindingRebindAndClosedObservation(t *testing.T) {
	ctx := context.Background()
	dir := privateTestDir(t)
	service, operator := reviewService(t, dir)
	s := service.Store
	ref := lifecycleReviewSession(t, s, operator, "")
	other := lifecycleReviewSession(t, s, operator, ref.WorkspaceID)
	a, ca := lifecycleReviewAgent(t, s, operator, ref, "bound-worker")
	b, cb := lifecycleReviewAgent(t, s, operator, other, "other-worker")
	binding := &RuntimeBinding{InstanceID: "review-instance", Endpoint: "review-endpoint", PaneID: "review-pane", TerminalID: "review-terminal", NativeSessionID: "review-native"}
	attach := AgentBindingRequest{Context: ref, OperationID: opaqueID(), AgentID: a.ResourceID, ExpectedRevision: 1, Runtime: binding}

	attached, err := s.BindAgent(ctx, ca.Token, "attach", attach)
	if err != nil {
		t.Fatal(err)
	}

	for _, duplicate := range []*RuntimeBinding{
		{InstanceID: binding.InstanceID},
		{InstanceID: "different-pane-instance", Endpoint: binding.Endpoint, PaneID: binding.PaneID},
		{InstanceID: "different-terminal-instance", Endpoint: binding.Endpoint, TerminalID: binding.TerminalID},
		{InstanceID: "different-native-instance", NativeSessionID: binding.NativeSessionID},
	} {
		_, err := s.BindAgent(ctx, cb.Token, "attach", AgentBindingRequest{Context: other, OperationID: opaqueID(), AgentID: b.ResourceID, ExpectedRevision: 1, Runtime: duplicate})
		reviewCode(t, err, "conflict")
	}

	_, err = s.BindAgent(ctx, ca.Token, "attach", AgentBindingRequest{Context: ref, OperationID: opaqueID(), AgentID: a.ResourceID, ExpectedRevision: attached.Revision, Runtime: &RuntimeBinding{InstanceID: "replacement"}})
	reviewCode(t, err, "conflict")
	newCredential := Credential{Kind: AgentCapability, Token: opaqueID()}
	rebind := AgentBindingRequest{Context: ref, OperationID: opaqueID(), AgentID: a.ResourceID, ExpectedRevision: attached.Revision, Runtime: binding, CredentialHash: tokenHash(newCredential.Token)}
	_, err = s.BindAgent(ctx, ca.Token, "rebind", rebind)
	reviewCode(t, err, "forbidden")

	rebound, err := s.BindAgent(ctx, operator.Token, "rebind", rebind)
	if err != nil {
		t.Fatal(err)
	}

	retry, err := s.BindAgent(ctx, operator.Token, "rebind", rebind)
	if err != nil || retry != rebound {
		t.Fatal("rebind retry rotated again")
	}

	_, err = s.InspectSession(ctx, ca.Token, ref)
	reviewCode(t, err, "unauthorized")
	_, err = s.BindAgent(ctx, ca.Token, "attach", attach)
	reviewCode(t, err, "unauthorized")

	closed, err := s.ChangeSession(ctx, operator.Token, "close", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}

	agent, err := s.ObserveAgent(ctx, newCredential.Token, AgentObserveRequest{Context: ref, AgentID: a.ResourceID, ExpectedRevision: rebound.Revision, RuntimeID: binding.InstanceID, State: "done"})
	if err != nil || agent.ID != a.ResourceID || agent.Revision != rebound.Revision || agent.ObservedAt == "" {
		t.Fatal("observation changed identity or failed while closed")
	}

	current, err := s.InspectSession(ctx, newCredential.Token, ref)
	if err != nil || !current.Closed || current.Revision != closed.Revision {
		t.Fatal("observation changed Session lifecycle")
	}

	public, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}

	for _, private := range []string{binding.InstanceID, binding.Endpoint, binding.NativeSessionID, newCredential.Token, `"binding"`, `"credential_hash"`, `"runtime_id"`} {
		if bytes.Contains(public, []byte(private)) {
			t.Fatal("private binding/credential leaked through Agent metadata")
		}
	}

	if err := service.Close(ctx); err != nil {
		t.Fatal(err)
	}

	restarted, _ := reviewService(t, dir)
	if _, err := restarted.Store.InspectSession(ctx, newCredential.Token, ref); err != nil {
		t.Fatal(err)
	}

	_, err = restarted.Store.InspectSession(ctx, ca.Token, ref)
	reviewCode(t, err, "unauthorized")
}

func TestLifecycleSpecDurableEnrollment(t *testing.T) {
	ctx := context.Background()
	service, operator := reviewService(t, privateTestDir(t))
	s := service.Store
	ref := lifecycleReviewSession(t, s, operator, "")
	request := AgentRegisterRequest{Context: ref, OperationID: opaqueID(), Name: "durable-worker"}
	parent := privateTestDir(t)
	path := filepath.Join(parent, "worker.json")

	hash, err := PrepareAgentCredential(path, s.id, "agent register", request.OperationID, request)
	if err != nil {
		t.Fatal(err)
	}

	credential, err := ReadCredential(path)
	if err != nil {
		t.Fatal(err)
	}

	originalFile, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	request.CredentialHash = hash

	first, err := s.RegisterAgent(ctx, operator.Token, request)
	if err != nil {
		t.Fatal(err)
	}

	unsigned := request
	unsigned.CredentialHash = ""

	recoveredHash, err := PrepareAgentCredential(path, s.id, "agent register", request.OperationID, unsigned)
	if err != nil || recoveredHash != hash {
		t.Fatal("lost-response recovery changed credential")
	}

	second, err := s.RegisterAgent(ctx, operator.Token, request)
	if err != nil || second != first {
		t.Fatal("registration retry duplicated identity")
	}

	afterFile, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(originalFile, afterFile) {
		t.Fatal("enrollment rewrote original token")
	}

	if _, err := s.InspectSession(ctx, credential.Token, ref); err != nil {
		t.Fatal("prepared credential cannot authenticate")
	}

	unsigned.Name = "changed-worker"
	_, err = PrepareAgentCredential(path, s.id, "agent register", request.OperationID, unsigned)
	reviewCode(t, err, "operation_conflict")

	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM agents WHERE session_id=?", ref.SessionID).Scan(&count); err != nil || count != 1 {
		t.Fatal("registration duplicate")
	}

	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(s.path + suffix)
		if err != nil {
			t.Fatal(err)
		}

		if bytes.Contains(data, []byte(credential.Token)) {
			t.Fatal("plaintext Agent token persisted server-side")
		}
	}

	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), credential.Token) {
		t.Fatal("credential leaked in receipt")
	}

	rebind := AgentBindingRequest{Context: ref, OperationID: opaqueID(), AgentID: first.ResourceID, ExpectedRevision: first.Revision, Runtime: &RuntimeBinding{InstanceID: "recovered-instance"}}
	newPath := filepath.Join(parent, "replacement.json")

	newHash, err := PrepareAgentCredential(newPath, s.id, "agent rebind", rebind.OperationID, rebind)
	if err != nil {
		t.Fatal(err)
	}

	unsignedRebind := rebind
	rebind.CredentialHash = newHash

	rebound, err := s.BindAgent(ctx, operator.Token, "rebind", rebind)
	if err != nil {
		t.Fatal(err)
	}

	resumedHash, err := PrepareAgentCredential(newPath, s.id, "agent rebind", rebind.OperationID, unsignedRebind)
	if err != nil || resumedHash != newHash {
		t.Fatal("lost rebind response changed replacement credential")
	}

	replayedRebind, err := s.BindAgent(ctx, operator.Token, "rebind", rebind)
	if err != nil || replayedRebind != rebound {
		t.Fatal("lost rebind response duplicated rotation")
	}

	_, err = s.InspectSession(ctx, credential.Token, ref)
	reviewCode(t, err, "unauthorized")

	replacement, err := ReadCredential(newPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.InspectSession(ctx, replacement.Token, ref); err != nil {
		t.Fatal(err)
	}

	if err := s.db.QueryRow("SELECT count(*) FROM capabilities WHERE kind='agent' AND subject_id=?", first.ResourceID).Scan(&count); err != nil || count != 1 {
		t.Fatal("rebind did not leave exactly one valid credential")
	}
}

func TestLifecycleSpecHTTPDispatch(t *testing.T) {
	dir := privateTestDir(t)
	service, operator := reviewService(t, dir)
	ref := lifecycleReviewSession(t, service.Store, operator, "")
	other := lifecycleReviewSession(t, service.Store, operator, ref.WorkspaceID)

	_, worker := lifecycleReviewAgent(t, service.Store, operator, ref, "http-worker")
	credentialPath := filepath.Join(dir, "worker.json")
	if err := writePrivate(credentialPath, worker, false); err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(dir, credentialPath, AgentCapability)
	if err != nil {
		t.Fatal(err)
	}

	data, err := client.Do(context.Background(), "POST", "/v1/session/inspect", ref)
	if err != nil {
		t.Fatal(err)
	}

	var envelope struct{ Data Session }
	if json.Unmarshal(data, &envelope) != nil || envelope.Data.ID != ref.SessionID {
		t.Fatal("HTTP session inspect returned wrong context")
	}

	_, err = client.Do(context.Background(), "POST", "/v1/session/inspect", other)
	reviewCode(t, err, "forbidden")
	_, err = client.Do(context.Background(), "POST", "/v1/session/close", SessionChangeRequest{Context: ref, OperationID: opaqueID(), ExpectedRevision: 1})
	reviewCode(t, err, "forbidden")
	_, err = client.Do(context.Background(), "POST", "/v1/agent/register", map[string]any{
		"workspace_id": ref.WorkspaceID, "session_id": ref.SessionID,
		"operation_id": opaqueID(), "name": "forged-actor", "credential_hash": tokenHash(opaqueID()), "role": "operator",
	})
	reviewCode(t, err, "invalid_input")
}

func TestLifecycleSpecPopulatedV1Upgrade(t *testing.T) {
	ctx := context.Background()

	dir := privateTestDir(t)
	path, err := ensureDatabaseFile(dir)
	if err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on&_synchronous=FULL&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}

	db.SetMaxOpenConns(1)
	defer db.Close()
	// This is the unchanged production schema-v1 migration, with actual persisted
	// credentials/effect/receipt, not an empty current-schema database relabelled v1.
	if _, err := db.Exec(migration1); err != nil {
		t.Fatal(err)
	}

	id := opaqueID()
	if _, err := db.Exec("INSERT INTO metadata VALUES('database_id',?)", id); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("PRAGMA user_version=1; PRAGMA application_id=1095980107; CREATE TABLE review_v1_effect(value);" + receiptActorStandIn + expiresStandIn + "CREATE TABLE expired_operations(actor TEXT,operation_id TEXT,outcome TEXT);"); err != nil {
		t.Fatal(err)
	}

	v1 := &Store{db: db, path: path, id: id}
	if err := v1.ensureOperator(ctx, dir); err != nil {
		t.Fatal(err)
	}

	operator, err := ReadCredential(filepath.Join(dir, "operator.json"))
	if err != nil {
		t.Fatal(err)
	}

	op := Operation{ID: opaqueID(), Name: "review.v1", Scope: "fixture", Payload: json.RawMessage(`{}`)}
	allow := func(context.Context, *sql.Tx, Principal, Receipt) error { return nil }

	original, err := v1.Mutate(ctx, operator.Token, op, allow, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		_, err := tx.ExecContext(ctx, "INSERT INTO review_v1_effect VALUES('preserved')")
		return Receipt{ResourceID: "fixture", Revision: 1, Outcome: "created"}, err
	})
	if err != nil {
		t.Fatal(err)
	}

	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("fixture is not schema v1")
	}

	if _, err := db.Exec(dropReceiptActor + dropExpires + "DROP TABLE expired_operations;"); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	service, sameCredential := reviewService(t, dir)
	if service.Store.id != id || sameCredential.Token != operator.Token {
		t.Fatal("migration changed database or Operator identity")
	}

	if err := service.Store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != schemaVersion {
		t.Fatal("upgrade did not reach the current schema")
	}

	replayed, err := service.Store.Mutate(ctx, operator.Token, op, allow, func(context.Context, *sql.Tx, Principal) (Receipt, error) {
		t.Error("v1 operation applied again after upgrade")
		return Receipt{}, failure("invalid_input", "unexpected apply")
	})
	if err != nil || replayed != original {
		t.Fatal("upgrade lost committed receipt")
	}

	reviewCount(t, service.Store.db, "review_v1_effect", 1)
	lifecycleReviewSession(t, service.Store, operator, "")
}
