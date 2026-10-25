package stack

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const migration2 = `
CREATE TABLE workspaces(id TEXT PRIMARY KEY, path TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE sessions(
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL REFERENCES workspaces(id), name TEXT NOT NULL,
 closed INTEGER NOT NULL DEFAULT 0 CHECK(closed IN (0,1)), orchestrator_id TEXT,
 revision INTEGER NOT NULL DEFAULT 1, read_revision INTEGER NOT NULL DEFAULT 1,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(id,workspace_id),
 FOREIGN KEY(orchestrator_id,id) REFERENCES agents(id,session_id) DEFERRABLE INITIALLY DEFERRED
) WITHOUT ROWID;
CREATE INDEX sessions_workspace ON sessions(workspace_id,id);
CREATE TABLE agents(
 id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, session_id TEXT NOT NULL,
 name TEXT NOT NULL, worktree_path TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 1,
 runtime_id TEXT, binding TEXT, runtime_state TEXT NOT NULL DEFAULT 'unknown', observed_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 UNIQUE(workspace_id,name), UNIQUE(id,session_id),
 FOREIGN KEY(session_id,workspace_id) REFERENCES sessions(id,workspace_id)
) WITHOUT ROWID;
CREATE INDEX agents_session ON agents(session_id,id);
CREATE UNIQUE INDEX agents_runtime ON agents(runtime_id) WHERE runtime_id IS NOT NULL;
CREATE TABLE runtime_claims(binding_key TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id)) WITHOUT ROWID;
CREATE INDEX runtime_claims_agent ON runtime_claims(agent_id);
CREATE TABLE lifecycle_history(
 sequence INTEGER PRIMARY KEY, resource_id TEXT NOT NULL, session_id TEXT REFERENCES sessions(id),
 actor_kind TEXT NOT NULL, actor_id TEXT NOT NULL, action TEXT NOT NULL, revision INTEGER NOT NULL, received_at TEXT NOT NULL
);
CREATE INDEX lifecycle_history_resource ON lifecycle_history(resource_id,sequence);
`

type Context struct {
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
}
type Workspace struct {
	ID        string `json:"id"`
	Path      string `json:"path"`
	CreatedAt string `json:"created_at"`
}
type Session struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	Name           string  `json:"name"`
	Closed         bool    `json:"closed"`
	OrchestratorID *string `json:"orchestrator_id"`
	Revision       int64   `json:"revision"`
	GraphRevision  int64   `json:"graph_revision"`
	ReadRevision   int64   `json:"read_revision"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}
type Agent struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	SessionID       string `json:"session_id"`
	Name            string `json:"name"`
	WorktreePath    string `json:"worktree_path,omitempty"`
	Revision        int64  `json:"revision"`
	RuntimeAttached bool   `json:"runtime_attached"`
	RuntimeState    string `json:"runtime_state"`
	ObservedAt      string `json:"observed_at,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

// RuntimeBinding is private identity data, never included in Agent metadata.
// InstanceID names the running instance, independently of reusable panes/names.
// Herdr verification is a later adapter's job; this slice executes no harness.
type RuntimeBinding struct {
	InstanceID      string `json:"instance_id"`
	HerdrAlias      string `json:"herdr_alias,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	PaneID          string `json:"pane_id,omitempty"`
	TerminalID      string `json:"terminal_id,omitempty"`
	NativeSessionID string `json:"native_session_id,omitempty"`
	Harness         string `json:"harness,omitempty"`
	HarnessVersion  string `json:"harness_version,omitempty"`
}
type WorkspaceCreateRequest struct {
	OperationID string `json:"operation_id"`
	Path        string `json:"path"`
}
type SessionCreateRequest struct {
	OperationID string `json:"operation_id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
}
type SessionChangeRequest struct {
	Context
	OperationID      string `json:"operation_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	AgentID          string `json:"agent_id,omitempty"`
}
type AgentRegisterRequest struct {
	Context
	OperationID    string `json:"operation_id"`
	Name           string `json:"name"`
	WorktreePath   string `json:"worktree_path,omitempty"`
	CredentialHash string `json:"credential_hash"`
}
type AgentBindingRequest struct {
	Context
	OperationID      string          `json:"operation_id"`
	AgentID          string          `json:"agent_id"`
	ExpectedRevision int64           `json:"expected_revision"`
	Runtime          *RuntimeBinding `json:"runtime"`
	CredentialHash   string          `json:"credential_hash,omitempty"`
}
type AgentObserveRequest struct {
	Context
	AgentID          string `json:"agent_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	RuntimeID        string `json:"runtime_id"`
	State            string `json:"state"`
}
type ListRequest struct {
	Context
	After string `json:"after,omitempty"`
	Limit int    `json:"limit,omitempty"`
}
type Page[T any] struct {
	Items []T    `json:"items"`
	Next  string `json:"next,omitempty"`
}

func now() string { return clockNow().UTC().Format(time.RFC3339Nano) }
func operator(p Principal) error {
	if p.Kind != OperatorCapability {
		return failure("forbidden", "Operator credential required")
	}
	return nil
}
func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || !utf8.ValidString(path) || len(path) > 1024 {
		return "", failure("invalid_input", "project/worktree path must be an absolute directory path of at most 1024 bytes")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", failure("invalid_input", "project/worktree directory does not exist")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() || len(canonical) > 1024 || !utf8.ValidString(canonical) {
		return "", failure("invalid_input", "project/worktree directory does not exist or exceeds path limit")
	}
	return canonical, nil
}
func expectedRevision(expected, current int64) error {
	if expected < 1 {
		return failure("invalid_input", "positive expected_revision required")
	}
	if expected != current {
		return &Error{Code: "conflict", Message: "resource revision changed", CurrentRevision: &current}
	}
	return nil
}
func recordLifecycle(ctx context.Context, tx *sql.Tx, p Principal, action, id, session string, revision int64) error {
	var sessionID any
	if session != "" {
		sessionID = session
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO lifecycle_history(resource_id,session_id,actor_kind,actor_id,action,revision,received_at) VALUES(?,?,?,?,?,?,?)", id, sessionID, p.Kind, p.SubjectID, action, revision, now())
	return err
}
func operation(id, name, scope string, payload any) Operation {
	b, _ := json.Marshal(payload)
	return Operation{ID: id, Name: name, Scope: scope, Payload: b}
}
func loadSession(ctx context.Context, tx *sql.Tx, id string) (Session, error) {
	var s Session
	var holder sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT id,workspace_id,name,closed,orchestrator_id,revision,read_revision,graph_revision,created_at,updated_at FROM sessions WHERE id=?", id).Scan(&s.ID, &s.WorkspaceID, &s.Name, &s.Closed, &holder, &s.Revision, &s.ReadRevision, &s.GraphRevision, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, failure("not_found", "Session not found")
	}
	if err != nil {
		return s, storageError()
	}
	if holder.Valid {
		s.OrchestratorID = &holder.String
	}
	return s, nil
}

// ReadSession checks explicit context and current identity inside the caller's
// transaction. It deliberately permits authorized reads after Session closure.
func ReadSession(ctx context.Context, tx *sql.Tx, p Principal, ref Context) (Session, error) {
	if !validToken(ref.WorkspaceID) || !validToken(ref.SessionID) {
		return Session{}, failure("invalid_input", "explicit Workspace and Session IDs required")
	}
	if p.Kind != OperatorCapability && p.SessionID != ref.SessionID {
		return Session{}, failure("forbidden", "credential is scoped to another Session")
	}
	s, err := loadSession(ctx, tx, ref.SessionID)
	if err != nil {
		return s, err
	}
	if s.WorkspaceID != ref.WorkspaceID {
		return s, failure("invalid_input", "Workspace and Session do not match")
	}
	if p.Kind == AgentCapability {
		var member int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE id=? AND session_id=? AND workspace_id=?", p.SubjectID, s.ID, s.WorkspaceID).Scan(&member); err != nil {
			return s, storageError()
		}
		if member != 1 {
			return s, failure("forbidden", "current Session registration required")
		}
	} else if p.Kind != OperatorCapability && p.Kind != BrowserCapability {
		return s, failure("forbidden", "Session read capability required")
	}
	return s, nil
}

// OpenSession is the common guard for later Task/mail mutations. Call it within
// Store.Mutate's apply callback; it requires no Orchestrator/runtime presence.
func OpenSession(ctx context.Context, tx *sql.Tx, p Principal, ref Context) (Session, error) {
	s, err := ReadSession(ctx, tx, p, ref)
	if err != nil {
		return s, err
	}
	if s.Closed {
		return s, failure("session_closed", "Session is closed")
	}
	return s, nil
}
func RequireOrchestrator(p Principal, s Session) error {
	if p.Kind != AgentCapability || s.OrchestratorID == nil || *s.OrchestratorID != p.SubjectID {
		return failure("forbidden", "current Session Orchestrator required")
	}
	return nil
}
func (s *Store) CreateWorkspace(ctx context.Context, token string, r WorkspaceCreateRequest) (Receipt, error) {
	// Preparation may fail after the original commit (e.g. a removed folder).
	// Only a new authorized apply consumes the error; replay does database work.
	path, prepareErr := canonicalDirectory(r.Path)
	return s.Mutate(ctx, token, operation(r.OperationID, "workspace.create", "workspaces", r), func(ctx context.Context, tx *sql.Tx, p Principal, receipt Receipt) error { return operator(p) }, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if err := operator(p); err != nil {
			return Receipt{}, err
		}
		if prepareErr != nil {
			return Receipt{}, prepareErr
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM workspaces WHERE path=?", path).Scan(&exists); err != nil {
			return Receipt{}, err
		}
		if exists != 0 {
			return Receipt{}, failure("conflict", "canonical project directory already registered")
		}
		id := opaqueID()
		_, err := tx.ExecContext(ctx, "INSERT INTO workspaces VALUES(?,?,?)", id, path, now())
		if err != nil {
			return Receipt{}, err
		}
		if err = recordLifecycle(ctx, tx, p, "workspace.create", id, "", 1); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: id, Revision: 1, Outcome: "created"}, nil
	})
}
func (s *Store) CreateSession(ctx context.Context, token string, r SessionCreateRequest) (Receipt, error) {
	return s.Mutate(ctx, token, operation(r.OperationID, "session.create", r.WorkspaceID, r), replaySession, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if err := operator(p); err != nil {
			return Receipt{}, err
		}
		if !validToken(r.WorkspaceID) || len(strings.TrimSpace(r.Name)) == 0 || len(r.Name) > 128 || !utf8.ValidString(r.Name) {
			return Receipt{}, failure("invalid_input", "Workspace ID and Session name (1–128 UTF-8 bytes) required")
		}
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM workspaces WHERE id=?", r.WorkspaceID).Scan(&exists); err != nil {
			return Receipt{}, err
		}
		if exists != 1 {
			return Receipt{}, failure("not_found", "Workspace not found")
		}
		id, timestamp := opaqueID(), now()
		_, err := tx.ExecContext(ctx, "INSERT INTO sessions(id,workspace_id,name,created_at,updated_at) VALUES(?,?,?,?,?)", id, r.WorkspaceID, r.Name, timestamp, timestamp)
		if err != nil {
			return Receipt{}, err
		}
		if err = recordLifecycle(ctx, tx, p, "session.create", id, id, 1); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: id, Revision: 1, Outcome: "created"}, nil
	})
}
func (s *Store) ChangeSession(ctx context.Context, token, action string, r SessionChangeRequest) (Receipt, error) {
	if action != "close" && action != "reopen" && action != "appoint" {
		return Receipt{}, failure("invalid_input", "unknown Session action")
	}
	if action != "appoint" && r.AgentID != "" {
		return Receipt{}, failure("invalid_input", "agent_id applies only to appointment")
	}
	return s.Mutate(ctx, token, operation(r.OperationID, "session."+action, r.SessionID, r), replaySession, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if err := operator(p); err != nil {
			return Receipt{}, err
		}
		session, err := ReadSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		if err = expectedRevision(r.ExpectedRevision, session.Revision); err != nil {
			return Receipt{}, err
		}
		var outcome string
		if action == "appoint" {
			if session.Closed {
				return Receipt{}, failure("session_closed", "Session is closed")
			}
			if _, err = agentInSession(ctx, tx, r.AgentID, session.ID); err != nil {
				return Receipt{}, err
			}
			if session.OrchestratorID != nil && *session.OrchestratorID == r.AgentID {
				return Receipt{ResourceID: session.ID, Revision: session.Revision, Outcome: "appointed"}, nil
			}
			_, err = tx.ExecContext(ctx, "UPDATE sessions SET orchestrator_id=?,revision=revision+1,read_revision=read_revision+1,updated_at=? WHERE id=?", r.AgentID, now(), session.ID)
			outcome = "appointed"
		} else {
			closed := action == "close"
			outcome = "reopened"
			if closed {
				outcome = "closed"
			}
			if session.Closed == closed {
				return Receipt{ResourceID: session.ID, Revision: session.Revision, Outcome: outcome}, nil
			}
			_, err = tx.ExecContext(ctx, "UPDATE sessions SET closed=?,revision=revision+1,read_revision=read_revision+1,updated_at=? WHERE id=?", closed, now(), session.ID)
		}
		if err != nil {
			return Receipt{}, err
		}
		if err = recordLifecycle(ctx, tx, p, "session."+action, session.ID, session.ID, session.Revision+1); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: session.ID, Revision: session.Revision + 1, Outcome: outcome}, nil
	})
}

var agentName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func validHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == hash
}
func installAgentHash(ctx context.Context, tx *sql.Tx, hash, subject, session string) error {
	if !validHash(hash) {
		return failure("invalid_input", "credential_hash must be a SHA-256 lowercase hex digest")
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM capabilities WHERE token_hash=?", hash).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return failure("conflict", "credential hash already registered")
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,'agent',?,?)", hash, subject, session)
	return err
}
func agentInSession(ctx context.Context, tx *sql.Tx, id, session string) (Agent, error) {
	var a Agent
	if !validToken(id) {
		return a, failure("invalid_input", "Agent ID required")
	}
	err := tx.QueryRowContext(ctx, "SELECT id,workspace_id,session_id,name,worktree_path,revision,runtime_id IS NOT NULL,runtime_state,observed_at,created_at,updated_at FROM agents WHERE id=? AND session_id=?", id, session).Scan(&a.ID, &a.WorkspaceID, &a.SessionID, &a.Name, &a.WorktreePath, &a.Revision, &a.RuntimeAttached, &a.RuntimeState, &a.ObservedAt, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return a, failure("not_found", "Agent not found in Session")
	}
	if err != nil {
		return a, storageError()
	}
	return a, nil
}
func bumpSessionRead(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, "UPDATE sessions SET read_revision=read_revision+1 WHERE id=?", id)
	return err
}
func replaySession(ctx context.Context, tx *sql.Tx, p Principal, r Receipt) error {
	session, err := loadSession(ctx, tx, r.ResourceID)
	if err != nil {
		return err
	}
	_, err = ReadSession(ctx, tx, p, Context{session.WorkspaceID, session.ID})
	return err
}
func replayAgent(ctx context.Context, tx *sql.Tx, p Principal, r Receipt) error {
	var ref Context
	err := tx.QueryRowContext(ctx, "SELECT workspace_id,session_id FROM agents WHERE id=?", r.ResourceID).Scan(&ref.WorkspaceID, &ref.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return failure("not_found", "Agent not found")
	}
	if err != nil {
		return storageError()
	}
	_, err = ReadSession(ctx, tx, p, ref)
	return err
}
func (s *Store) RegisterAgent(ctx context.Context, token string, r AgentRegisterRequest) (Receipt, error) {
	var path string
	var prepareErr error
	if r.WorktreePath != "" {
		path, prepareErr = canonicalDirectory(r.WorktreePath)
	}
	return s.Mutate(ctx, token, operation(r.OperationID, "agent.register", r.SessionID, r), replayAgent, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		session, err := OpenSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		if p.Kind != OperatorCapability {
			if err = RequireOrchestrator(p, session); err != nil {
				return Receipt{}, err
			}
		}
		if !agentName.MatchString(r.Name) {
			return Receipt{}, failure("invalid_input", "Agent name must match [A-Za-z0-9][A-Za-z0-9_-]{0,63}")
		}
		if prepareErr != nil {
			return Receipt{}, prepareErr
		}
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE workspace_id=? AND name=?", r.WorkspaceID, r.Name).Scan(&exists); err != nil {
			return Receipt{}, err
		}
		if exists != 0 {
			return Receipt{}, failure("conflict", "Agent name already registered in Workspace")
		}
		id, timestamp := opaqueID(), now()
		if err = installAgentHash(ctx, tx, r.CredentialHash, id, session.ID); err != nil {
			return Receipt{}, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO agents(id,workspace_id,session_id,name,worktree_path,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", id, r.WorkspaceID, session.ID, r.Name, path, timestamp, timestamp)
		if err != nil {
			return Receipt{}, err
		}
		if err = bumpSessionRead(ctx, tx, session.ID); err != nil {
			return Receipt{}, err
		}
		if err = recordLifecycle(ctx, tx, p, "agent.register", id, session.ID, 1); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: id, Revision: 1, Outcome: "registered"}, nil
	})
}
func validateBinding(binding *RuntimeBinding, required bool) error {
	if binding == nil {
		if required {
			return failure("invalid_input", "runtime binding required")
		}
		return nil
	}
	if binding.InstanceID == "" || len(binding.InstanceID) > 128 || !utf8.ValidString(binding.InstanceID) {
		return failure("invalid_input", "runtime instance_id must contain 1–128 UTF-8 bytes")
	}
	for _, field := range []string{binding.HerdrAlias, binding.Endpoint, binding.PaneID, binding.TerminalID, binding.NativeSessionID, binding.Harness, binding.HarnessVersion} {
		if len(field) > 512 || !utf8.ValidString(field) {
			return failure("invalid_input", "runtime metadata exceeds limit")
		}
	}
	return nil
}
func (s *Store) BindAgent(ctx context.Context, token, action string, r AgentBindingRequest) (Receipt, error) {
	if action != "attach" && action != "rebind" {
		return Receipt{}, failure("invalid_input", "unknown binding action")
	}
	if err := validateBinding(r.Runtime, action == "attach"); err != nil {
		return Receipt{}, err
	}
	if action == "attach" && r.CredentialHash != "" {
		return Receipt{}, failure("invalid_input", "attach cannot change credentials")
	}
	return s.Mutate(ctx, token, operation(r.OperationID, "agent."+action, r.SessionID, r), replayAgent, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		session, err := OpenSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		if action == "rebind" {
			if err = operator(p); err != nil {
				return Receipt{}, err
			}
		} else if p.Kind != AgentCapability || p.SubjectID != r.AgentID {
			return Receipt{}, failure("forbidden", "only that Agent can attach its fresh registration")
		}
		agent, err := agentInSession(ctx, tx, r.AgentID, session.ID)
		if err != nil {
			return Receipt{}, err
		}
		if err = expectedRevision(r.ExpectedRevision, agent.Revision); err != nil {
			return Receipt{}, err
		}
		if action == "attach" && agent.RuntimeAttached {
			return Receipt{}, failure("conflict", "established binding requires Operator rebind")
		}
		var runtimeID, binding any
		if r.Runtime != nil {
			var occupied int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE runtime_id=? AND id!=?", r.Runtime.InstanceID, agent.ID).Scan(&occupied); err != nil {
				return Receipt{}, err
			}
			if occupied != 0 {
				return Receipt{}, failure("conflict", "runtime instance already attached")
			}
			runtimeID = r.Runtime.InstanceID
			b, _ := json.Marshal(r.Runtime)
			binding = string(b)
		}
		if action == "rebind" {
			if !validHash(r.CredentialHash) {
				return Receipt{}, failure("invalid_input", "new credential_hash required")
			}
			// Check/install first so an already-used hash can never preserve an old token.
			if err = installAgentHash(ctx, tx, r.CredentialHash, agent.ID, session.ID); err != nil {
				return Receipt{}, err
			}
			if _, err = tx.ExecContext(ctx, "DELETE FROM capabilities WHERE kind='agent' AND subject_id=? AND token_hash!=?", agent.ID, r.CredentialHash); err != nil {
				return Receipt{}, err
			}
		}
		if action == "rebind" {
			if _, err = tx.ExecContext(ctx, "DELETE FROM runtime_claims WHERE agent_id=?", agent.ID); err != nil {
				return Receipt{}, err
			}
		}
		for _, key := range concreteRuntimeKeys(r.Runtime) {
			var occupied int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runtime_claims WHERE binding_key=?", key).Scan(&occupied); err != nil {
				return Receipt{}, err
			}
			if occupied != 0 {
				return Receipt{}, failure("conflict", "concrete runtime target already attached; Operator must clear or replace its established binding")
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO runtime_claims VALUES(?,?)", key, agent.ID); err != nil {
				return Receipt{}, err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE agents SET runtime_id=?,binding=?,runtime_state='unknown',observed_at='',revision=revision+1,updated_at=? WHERE id=?", runtimeID, binding, now(), agent.ID)
		if err != nil {
			return Receipt{}, err
		}
		// A new binding gets its pending notices tried at once; the commit
		// wakes the notifier. No timer retries a parked binding.
		if _, err = tx.ExecContext(ctx, "UPDATE notice_targets SET failures=0,next_attempt=0 WHERE session_id=? AND recipient_id=? AND (failures!=0 OR next_attempt!=0)", session.ID, agent.ID); err != nil {
			return Receipt{}, err
		}
		if err = bumpSessionRead(ctx, tx, session.ID); err != nil {
			return Receipt{}, err
		}
		if err = recordLifecycle(ctx, tx, p, "agent."+action, agent.ID, session.ID, agent.Revision+1); err != nil {
			return Receipt{}, err
		}
		outcome := "attached"
		if action == "rebind" {
			outcome = "rebound"
		}
		return Receipt{ResourceID: agent.ID, Revision: agent.Revision + 1, Outcome: outcome}, nil
	})
}

// ObserveAgent changes only the latest runtime observation, including while
// closed. It never changes identity revisions, role, Session lifecycle or work.
// Observations are replaceable metadata, not retained domain operation history.
func (s *Store) ObserveAgent(ctx context.Context, token string, r AgentObserveRequest) (Agent, error) {
	empty := Agent{}
	switch r.State {
	case "unknown", "working", "idle", "done", "stopped", "blocked":
	default:
		return empty, failure("invalid_input", "unknown runtime observation state")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, storageError()
	}
	defer tx.Rollback()
	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return empty, err
	}
	if p.Kind != OperatorCapability && (p.Kind != AgentCapability || p.SubjectID != r.AgentID) {
		return empty, failure("forbidden", "Operator or that Agent required")
	}
	if _, err = ReadSession(ctx, tx, p, r.Context); err != nil {
		return empty, err
	}
	agent, err := agentInSession(ctx, tx, r.AgentID, r.SessionID)
	if err != nil {
		return empty, err
	}
	if err = expectedRevision(r.ExpectedRevision, agent.Revision); err != nil {
		return empty, err
	}
	var runtimeID sql.NullString
	if err = tx.QueryRowContext(ctx, "SELECT runtime_id FROM agents WHERE id=?", agent.ID).Scan(&runtimeID); err != nil {
		return empty, storageError()
	}
	if !runtimeID.Valid || r.RuntimeID != runtimeID.String {
		return empty, failure("conflict", "runtime binding changed or is unattached")
	}
	agent.RuntimeState = r.State
	agent.ObservedAt = now()
	if _, err = tx.ExecContext(ctx, "UPDATE agents SET runtime_state=?,observed_at=? WHERE id=?", r.State, agent.ObservedAt, agent.ID); err != nil {
		return empty, storageError()
	}
	if err = bumpSessionRead(ctx, tx, r.SessionID); err != nil {
		return empty, storageError()
	}
	if tx.Commit() != nil {
		return empty, storageError()
	}
	return agent, nil
}

// Reserve all supplied stable target identifiers, independently of the mutable
// synthetic InstanceID/alias. Incomplete metadata has only the claims available;
// these caller assertions are not harness verification or delivery authority.
func concreteRuntimeKeys(binding *RuntimeBinding) []string {
	keys := []string{}
	if binding == nil {
		return keys
	}
	add := func(kind string, values ...string) {
		b, _ := json.Marshal(values)
		digest := sha256.Sum256(b)
		keys = append(keys, kind+":"+hex.EncodeToString(digest[:]))
	}
	if binding.Endpoint != "" && binding.PaneID != "" {
		add("pane", binding.Endpoint, binding.PaneID)
	}
	if binding.Endpoint != "" && binding.TerminalID != "" {
		add("terminal", binding.Endpoint, binding.TerminalID)
	}
	if binding.NativeSessionID != "" {
		add("native", binding.NativeSessionID)
	}
	return keys
}
