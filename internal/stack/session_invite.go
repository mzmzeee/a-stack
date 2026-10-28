package stack

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"unicode/utf8"
)

const migration13 = `
CREATE TABLE session_operators(token_hash TEXT PRIMARY KEY REFERENCES capabilities(token_hash) ON DELETE CASCADE, session_id TEXT NOT NULL REFERENCES sessions(id), login TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE session_invites(session_id TEXT NOT NULL REFERENCES sessions(id), login TEXT NOT NULL, PRIMARY KEY(session_id,login)) WITHOUT ROWID;
CREATE TABLE session_links(database_id TEXT NOT NULL, session_id TEXT NOT NULL, workspace_id TEXT NOT NULL, local_workspace_id TEXT NOT NULL REFERENCES workspaces(id), remote TEXT NOT NULL, credential_file TEXT NOT NULL, name TEXT NOT NULL, PRIMARY KEY(database_id,session_id)) WITHOUT ROWID;
CREATE TABLE workspace_repositories(repository TEXT PRIMARY KEY, workspace_id TEXT NOT NULL UNIQUE REFERENCES workspaces(id)) WITHOUT ROWID;
CREATE TABLE session_transfers(id TEXT PRIMARY KEY, session_id TEXT NOT NULL, direction TEXT NOT NULL CHECK(direction IN ('in','out')), state TEXT NOT NULL, read_revision INTEGER NOT NULL, was_closed INTEGER NOT NULL DEFAULT 0, peer_database TEXT NOT NULL, remote TEXT NOT NULL, payload TEXT NOT NULL);
CREATE TABLE session_locations(session_id TEXT PRIMARY KEY REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED, transfer_id TEXT NOT NULL REFERENCES session_transfers(id), state TEXT NOT NULL CHECK(state IN ('staged','sealed','active')), peer_database TEXT NOT NULL, remote TEXT NOT NULL) WITHOUT ROWID;
`

type SessionInviteRequest struct {
	Context
	OperationID string `json:"operation_id"`
	Login       string `json:"login"`
}

type SessionJoinRequest struct {
	SessionID      string `json:"session_id"`
	CredentialHash string `json:"credential_hash"`
}

func sessionOperator(p Principal, session string) error {
	if p.Kind != OperatorCapability || (p.SessionID != "" && p.SessionID != session) {
		return failure("forbidden", "Operator credential for this Session required")
	}
	return nil
}

func (s *Store) InviteSession(ctx context.Context, token string, r SessionInviteRequest) (Receipt, error) {
	authorize := func(ctx context.Context, tx *sql.Tx, p Principal) (Session, error) {
		if err := sessionOperator(p, r.SessionID); err != nil {
			return Session{}, err
		}
		return ReadSession(ctx, tx, p, r.Context)
	}
	return s.Mutate(ctx, token, operation(r.OperationID, "session.invite", r.SessionID, r), func(ctx context.Context, tx *sql.Tx, p Principal, _ Receipt) error {
		_, err := authorize(ctx, tx, p)
		return err
	}, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		session, err := authorize(ctx, tx, p)
		if err != nil {
			return Receipt{}, err
		}
		if r.Login == "" || len(r.Login) > 320 || !utf8.ValidString(r.Login) || strings.ContainsFunc(r.Login, func(r rune) bool { return r <= 32 || r == 127 }) {
			return Receipt{}, failure("invalid_input", "exact Tailscale login required")
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO session_invites(session_id,login) VALUES(?,?) ON CONFLICT DO NOTHING", session.ID, r.Login); err != nil {
			return Receipt{}, err
		}
		if err = recordLifecycle(ctx, tx, p, "session.invite", session.ID, session.ID, session.Revision); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: session.ID, Revision: session.Revision, Outcome: "invited"}, nil
	})
}

// JoinSession is the only bearer-free data endpoint. Only prepareRequest can
// supply its trusted Serve identity; an invitation grants one Session, not the server.
func (s *Store) JoinSession(ctx context.Context, r SessionJoinRequest) (Context, error) {
	actor, ok := ActorFromContext(ctx)
	if !ok || actor.Login == "" {
		return Context{}, failure("forbidden", "trusted Tailscale identity required")
	}
	if !validToken(r.SessionID) || !validHash(r.CredentialHash) {
		return Context{}, failure("invalid_input", "Session ID and credential hash required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Context{}, storageError(err)
	}
	defer tx.Rollback()
	var invited bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM session_invites WHERE session_id=? AND login=?)", r.SessionID, actor.Login).Scan(&invited); err != nil {
		return Context{}, storageError(err)
	}
	if !invited {
		return Context{}, failure("forbidden", "this login has not been invited to this Session")
	}
	session, err := loadSession(ctx, tx, r.SessionID)
	if err != nil {
		return Context{}, err
	}
	if err = sessionTransferGuard(ctx, tx, session.ID); err != nil {
		return Context{}, err
	}
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM capabilities WHERE token_hash=?)", r.CredentialHash).Scan(&exists); err != nil {
		return Context{}, storageError(err)
	}
	if exists {
		var same bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM session_operators WHERE token_hash=? AND session_id=? AND login=?)", r.CredentialHash, session.ID, actor.Login).Scan(&same); err != nil {
			return Context{}, storageError(err)
		}
		if !same {
			return Context{}, failure("conflict", "credential hash already exists")
		}
	} else {
		if _, err = tx.ExecContext(ctx, "INSERT INTO capabilities(token_hash,kind,subject_id,session_id) VALUES(?,'operator',?,'')", r.CredentialHash, opaqueID()); err != nil {
			return Context{}, storageError(err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO session_operators(token_hash,session_id,login) VALUES(?,?,?)", r.CredentialHash, session.ID, actor.Login); err != nil {
			return Context{}, storageError(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return Context{}, storageError(err)
	}
	return Context{WorkspaceID: session.WorkspaceID, SessionID: session.ID}, nil
}

func (s *Service) sessionJoinHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST required"))
		return
	}
	var request SessionJoinRequest
	if err := requestJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	result, err := s.Store.JoinSession(r.Context(), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}
