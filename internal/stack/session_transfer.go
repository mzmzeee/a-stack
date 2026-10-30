package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// TransferBundle is a Session snapshot, never a database backup. The fixed table
// list excludes capabilities, browser grants and machine-local delivery state.
type TransferBundle struct {
	ReleaseHash    string            `json:"release_hash"`
	Released       map[string]string `json:"released,omitempty"`
	Schema         int               `json:"schema"`
	SourceDatabase string            `json:"source_database"`
	TransferID     string            `json:"transfer_id"`
	Repository     string            `json:"repository"`
	Session        Session           `json:"session"`
	Orders         StandingOrders    `json:"orders"`
	Tables         []TransferTable   `json:"tables"`
}
type TransferTable struct {
	Name string  `json:"name"`
	Rows [][]any `json:"rows"`
}
type TransferExportRequest struct {
	Context
	TransferID string `json:"transfer_id"`
	Repository string `json:"repository"`
}
type TransferImportRequest struct {
	Machine       string            `json:"machine,omitempty"`
	LocalRuntime  bool              `json:"local_runtime,omitempty"`
	Bundle        TransferBundle    `json:"bundle"`
	WorkspacePath string            `json:"workspace_path,omitempty"`
	AgentHashes   map[string]string `json:"agent_hashes"`
}
type TransferChangeRequest struct {
	Context
	TransferID          string `json:"transfer_id"`
	DestinationDatabase string `json:"destination_database,omitempty"`
	Remote              string `json:"remote,omitempty"`
}
type TransferStatus struct {
	Context
	TransferID string `json:"transfer_id"`
	State      string `json:"state"`
	DatabaseID string `json:"database_id"`
	Remote     string `json:"remote,omitempty"`
}

type transferTableSpec struct{ name, where, order string }

var transferTables = []transferTableSpec{
	{"sessions", "id=?", "id"},
	{"agents", "session_id=?", "id"},
	{"tasks", "session_id=?", "id"},
	{"task_content", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "id"},
	{"task_assignees", "session_id=?", "task_id,agent_id"},
	{"task_questions", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "id"},
	{"task_references", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "id"},
	{"task_history", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "sequence"},
	{"dependencies", "session_id=?", "id"},
	{"graph_history", "session_id=?", "sequence"},
	{"mail_messages", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "task_id,sequence"},
	{"mail_mentions", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "message_id,recipient_id"},
	{"mail_acks", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "message_id,recipient_id"},
	{"task_claims", "task_id IN (SELECT id FROM tasks WHERE session_id=?)", "task_id,path"},
	{"lifecycle_history", "session_id=?", "sequence"},
	{"operation_receipts", "scope=?", "received_at,actor,operation_id"},
	{"expired_operations", "actor IN (SELECT 'agent:'||id FROM agents WHERE session_id=?)", "actor,operation_id"},
	{"session_invites", "session_id=?", "login"},
}

func transferRows(ctx context.Context, tx *sql.Tx, spec transferTableSpec, session string) ([]string, [][]any, error) {
	rows, err := tx.QueryContext(ctx, "SELECT * FROM "+spec.name+" WHERE "+spec.where+" ORDER BY "+spec.order, session)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	result := [][]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}

		if err = rows.Scan(dest...); err != nil {
			return nil, nil, err
		}

		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = string(b)
			}
		}

		result = append(result, values)
	}

	return columns, result, rows.Err()
}

func (s *Store) ExportTransfer(ctx context.Context, token string, r TransferExportRequest) (TransferBundle, error) {
	var result TransferBundle
	if !validToken(r.TransferID) || r.Repository == "" || len(r.Repository) > 4096 {
		return result, failure("invalid_input", "transfer ID and normalized repository required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, storageError(err)
	}
	defer tx.Rollback()

	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return result, err
	}

	if err = sessionOperator(p, r.SessionID); err != nil {
		return result, err
	}

	session, err := ReadSession(ctx, tx, p, r.Context)
	if err != nil {
		return result, err
	}

	var payload, state string
	err = tx.QueryRowContext(ctx, "SELECT payload,state FROM session_transfers WHERE id=? AND session_id=? AND direction='out'", r.TransferID, r.SessionID).Scan(&payload, &state)
	if err == nil {
		if state != "prepared" {
			return result, failure("conflict", "transfer has been superseded; resume the current transfer")
		}

		if json.Unmarshal([]byte(payload), &result) != nil || result.Repository != r.Repository {
			return result, failure("conflict", "transfer ID already used")
		}

		return result, nil
	}

	if err != sql.ErrNoRows {
		return result, storageError(err)
	}

	if _, err = tx.ExecContext(ctx, "INSERT INTO workspace_repositories(repository,workspace_id) VALUES(?,?) ON CONFLICT(repository) DO NOTHING", r.Repository, session.WorkspaceID); err != nil {
		return result, failure("conflict", "Workspace repository identity differs")
	}

	var matched string
	if err = tx.QueryRowContext(ctx, "SELECT workspace_id FROM workspace_repositories WHERE repository=?", r.Repository).Scan(&matched); err != nil || matched != session.WorkspaceID {
		return result, failure("conflict", "repository already belongs to another Workspace")
	}

	orders, err := loadOrders(ctx, tx, session.WorkspaceID)
	if err != nil {
		return result, err
	}

	// Revoke earlier owners in the same transaction that issues the new snapshot.
	// Only then disclose their release proofs, allowing destinations to replace staging.
	if _, err = tx.ExecContext(ctx, "UPDATE session_transfers SET state='superseded' WHERE session_id=? AND direction='out' AND state='prepared'", session.ID); err != nil {
		return result, storageError(err)
	}

	released := map[string]string{}

	rows, err := tx.QueryContext(ctx, "SELECT id,release_token FROM session_transfers WHERE session_id=? AND direction='out' AND state='superseded'", session.ID)
	if err != nil {
		return result, storageError(err)
	}

	for rows.Next() {
		var id, proof string
		if err = rows.Scan(&id, &proof); err != nil {
			rows.Close()
			return result, storageError(err)
		}

		released[id] = proof
	}

	err = rows.Err()
	rows.Close()

	if err != nil {
		return result, storageError(err)
	}

	release := opaqueID()

	result = TransferBundle{ReleaseHash: tokenHash(release), Released: released, Schema: schemaVersion, SourceDatabase: s.id, TransferID: r.TransferID, Repository: r.Repository, Session: session, Orders: orders}
	for _, spec := range transferTables {
		_, rows, err := transferRows(ctx, tx, spec, session.ID)
		if err != nil {
			return result, storageError(err)
		}

		result.Tables = append(result.Tables, TransferTable{Name: spec.name, Rows: rows})
	}

	data, err := json.Marshal(result)
	if err != nil {
		return result, storageError(err)
	}

	if len(data) > MaxTransferBytes-65536 {
		return result, failure("invalid_input", "Session exceeds the transfer size limit")
	}

	_, err = tx.ExecContext(ctx, "INSERT INTO session_transfers(id,session_id,direction,state,read_revision,peer_database,remote,payload,release_token) VALUES(?,?,'out','prepared',?,'','',?,?)", r.TransferID, session.ID, session.ReadRevision, string(data), release)
	if err == nil {
		err = tx.Commit()
	}

	return result, storageOrNil(err)
}

func storageOrNil(err error) error {
	if err != nil {
		return storageError(err)
	}

	return nil
}

func transferStatus(ctx context.Context, tx *sql.Tx, id string) (TransferStatus, error) {
	var status TransferStatus
	err := tx.QueryRowContext(ctx, "SELECT t.id,t.session_id,s.workspace_id,t.state,t.peer_database,t.remote FROM session_transfers t JOIN sessions s ON s.id=t.session_id WHERE t.id=?", id).Scan(&status.TransferID, &status.SessionID, &status.WorkspaceID, &status.State, &status.DatabaseID, &status.Remote)
	if err == sql.ErrNoRows {
		return status, failure("not_found", "transfer not found")
	}

	return status, storageOrNil(err)
}

func (s *Store) ImportTransfer(ctx context.Context, token string, r TransferImportRequest) (TransferStatus, error) {
	var result TransferStatus
	b := r.Bundle
	if r.Machine != "" {
		if err := validPlainText("machine", r.Machine, 256); err != nil {
			return result, err
		}
	}

	if !validHash(b.ReleaseHash) || b.Schema != schemaVersion || !validToken(b.TransferID) || !validToken(b.SourceDatabase) || b.SourceDatabase == s.id || !validToken(b.Session.ID) || b.Repository == "" || len(b.Repository) > 4096 || len(b.Tables) != len(transferTables) {
		return result, failure("invalid_input", "invalid Session transfer snapshot")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, storageError(err)
	}
	defer tx.Rollback()

	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return result, err
	}

	if err = operator(p); err != nil {
		return result, err
	}

	if err = validateTransferSnapshot(ctx, tx, b); err != nil {
		return result, err
	}

	// The entire incoming graph is validated by SQLite at commit, including
	// references from Tasks back to their text rows and the Orchestrator seat.
	if _, err = tx.ExecContext(ctx, "PRAGMA defer_foreign_keys=ON"); err != nil {
		return result, storageError(err)
	}

	payload, err := json.Marshal(r)
	if err != nil {
		return result, storageError(err)
	}

	digest := tokenHash(string(payload))

	var oldDigest string
	err = tx.QueryRowContext(ctx, "SELECT payload FROM session_transfers WHERE id=?", b.TransferID).Scan(&oldDigest)
	if err == nil {
		if oldDigest != digest {
			return result, failure("conflict", "transfer ID already used with another snapshot")
		}

		return transferStatus(ctx, tx, b.TransferID)
	}

	if err != sql.ErrNoRows {
		return result, storageError(err)
	}

	workspace := ""

	err = tx.QueryRowContext(ctx, "SELECT workspace_id FROM workspace_repositories WHERE repository=?", b.Repository).Scan(&workspace)
	if err != nil && err != sql.ErrNoRows {
		return result, storageError(err)
	}

	if workspace == "" {
		workspace = opaqueID()

		path := r.WorkspacePath
		if path == "" {
			path = "shared:" + b.Repository
		} else {
			path, err = canonicalDirectory(path)
			if err != nil {
				return result, err
			}
		}

		var existing string
		err = tx.QueryRowContext(ctx, "SELECT id FROM workspaces WHERE path=?", path).Scan(&existing)
		if err == nil {
			workspace = existing
		} else if err == sql.ErrNoRows {
			_, err = tx.ExecContext(ctx, "INSERT INTO workspaces(id,path,created_at) VALUES(?,?,?)", workspace, path, now())
		}

		if err != nil {
			return result, storageError(err)
		}

		if _, err = tx.ExecContext(ctx, "INSERT INTO workspace_repositories(repository,workspace_id) VALUES(?,?)", b.Repository, workspace); err != nil {
			return result, storageError(err)
		}
	}

	currentOrders, err := loadOrders(ctx, tx, workspace)
	if err != nil {
		return result, err
	}

	if currentOrders.Body != "" && currentOrders.Body != b.Orders.Body {
		return result, failure("conflict", "destination Workspace standing orders differ")
	}

	if currentOrders.Body == "" && b.Orders.Body != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO workspace_orders(workspace_id,body,revision,updated_at) VALUES(?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET body=excluded.body,revision=excluded.revision,updated_at=excluded.updated_at", workspace, b.Orders.Body, b.Orders.Revision, b.Orders.UpdatedAt); err != nil {
			return result, storageError(err)
		}
	}

	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sessions WHERE id=?)", b.Session.ID).Scan(&exists); err != nil {
		return result, storageError(err)
	}

	if exists {
		var returning bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM session_locations WHERE session_id=? AND state IN ('sealed','staged') AND peer_database=?)", b.Session.ID, b.SourceDatabase).Scan(&returning); err != nil {
			return result, storageError(err)
		}

		if !returning {
			return result, failure("conflict", "Session ID already exists")
		}

		var previous, releaseHash string

		err = tx.QueryRowContext(ctx, "SELECT t.id,t.release_hash FROM session_locations l JOIN session_transfers t ON t.id=l.transfer_id WHERE l.session_id=? AND l.state='staged'", b.Session.ID).Scan(&previous, &releaseHash)
		if err != nil && err != sql.ErrNoRows {
			return result, storageError(err)
		}

		if err == nil && (!validToken(b.Released[previous]) || tokenHash(b.Released[previous]) != releaseHash) {
			return result, failure("conflict", "staged transfer still owns this Session; source release proof required")
		}

		if _, err = tx.ExecContext(ctx, "UPDATE session_transfers SET state='superseded' WHERE id IN (SELECT transfer_id FROM session_locations WHERE session_id=? AND state='staged')", b.Session.ID); err != nil {
			return result, storageError(err)
		}

		if err = clearTransferredSession(ctx, tx, b.Session.ID); err != nil {
			return result, storageError(err)
		}
	}

	for i, spec := range transferTables {
		table := b.Tables[i]
		if table.Name != spec.name {
			return result, failure("invalid_input", "transfer tables do not match this schema")
		}

		columns, _, err := transferRows(ctx, tx, spec, b.Session.ID)
		if err != nil {
			return result, storageError(err)
		}

		marks := strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")
		query := "INSERT INTO " + spec.name + "(" + strings.Join(columns, ",") + ") VALUES(" + marks + ")"
		for _, row := range table.Rows {
			if len(row) != len(columns) {
				return result, failure("invalid_input", "transfer row does not match schema")
			}

			values := append([]any(nil), row...)
			for j, col := range columns {
				if (spec.name == "mail_mentions" && col == "notice_state" || spec.name == "mail_acks" && col == "reminder_state") && values[j] == "submitting" {
					values[j] = "uncertain"
				}

				if col == "workspace_id" {
					values[j] = workspace
				}

				if col == "sequence" && (spec.name == "lifecycle_history" || spec.name == "task_history" || spec.name == "graph_history") {
					values[j] = nil
				}

				if spec.name == "agents" {
					switch col {
					case "binding":
						if values[j] == nil {
							values[j] = "{}"
						}

						if encoded, ok := values[j].(string); ok {
							var binding RuntimeBinding
							if json.Unmarshal([]byte(encoded), &binding) != nil {
								return result, failure("invalid_input", "invalid runtime binding in transfer")
							}

							if binding.Machine == "" {
								binding.Machine = r.Machine
							}

							binding.RemoteMachine = !r.LocalRuntime || binding.Machine != r.Machine

							encoded, err := json.Marshal(binding)
							if err != nil {
								return result, storageError(err)
							}

							values[j] = string(encoded)
						}
					case "runtime_state":
						values[j] = "unknown"
					case "observed_at":
						values[j] = ""
					}
				}

				if number, ok := values[j].(json.Number); ok {
					value, e := number.Int64()
					if e != nil {
						return result, failure("invalid_input", "invalid transfer integer")
					}

					values[j] = value
				}
			}

			if _, err = tx.ExecContext(ctx, query, values...); err != nil {
				return result, failure("conflict", "transfer row collides or violates a data constraint")
			}
		}
	}

	var agentCount int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM agents WHERE session_id=?", b.Session.ID).Scan(&agentCount); err != nil {
		return result, storageError(err)
	}

	if agentCount != len(r.AgentHashes) {
		return result, failure("invalid_input", "fresh credentials required for every transferred Agent")
	}

	for agent, hash := range r.AgentHashes {
		if !validHash(hash) {
			return result, failure("invalid_input", "invalid Agent credential hash")
		}

		if _, err = agentInSession(ctx, tx, agent, b.Session.ID); err != nil {
			return result, err
		}

		if err = installAgentHash(ctx, tx, hash, agent, b.Session.ID); err != nil {
			return result, storageError(err)
		}
	}

	_, err = tx.ExecContext(ctx, "INSERT INTO session_transfers(id,session_id,direction,state,read_revision,peer_database,remote,payload,release_hash) VALUES(?,?,'in','staged',?,?,'',?,?)", b.TransferID, b.Session.ID, b.Session.ReadRevision, b.SourceDatabase, digest, b.ReleaseHash)
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO session_locations(session_id,transfer_id,state,peer_database,remote) VALUES(?,?,'staged',?,'') ON CONFLICT(session_id) DO UPDATE SET transfer_id=excluded.transfer_id,state=excluded.state,peer_database=excluded.peer_database,remote=''", b.Session.ID, b.TransferID, b.SourceDatabase)
	}

	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE session_transfers SET was_closed=? WHERE id=?", b.Session.Closed, b.TransferID)
	}

	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE sessions SET closed=1 WHERE id=?", b.Session.ID)
	}

	if err != nil {
		return result, storageError(err)
	}

	result, err = transferStatus(ctx, tx, b.TransferID)
	if err == nil {
		err = tx.Commit()
	}

	return result, storageOrNil(err)
}

func clearTransferredSession(ctx context.Context, tx *sql.Tx, session string) error {
	// Remove only the sealed recovery copy. All imported rows are restored in
	// the same transaction; any constraint failure restores the old copy too.
	for _, query := range []string{
		"DELETE FROM notice_ack_refs WHERE batch_id IN (SELECT id FROM notice_batches WHERE recipient_id IN (SELECT id FROM agents WHERE session_id=?))",
		"DELETE FROM notice_batch_refs WHERE batch_id IN (SELECT id FROM notice_batches WHERE recipient_id IN (SELECT id FROM agents WHERE session_id=?))",
		"DELETE FROM notice_batches WHERE recipient_id IN (SELECT id FROM agents WHERE session_id=?)",
		"DELETE FROM notice_targets WHERE recipient_id IN (SELECT id FROM agents WHERE session_id=?)",
		"DELETE FROM runtime_claims WHERE agent_id IN (SELECT id FROM agents WHERE session_id=?)",
		"DELETE FROM capabilities WHERE token_hash IN (SELECT token_hash FROM session_operators WHERE session_id=?)",
		"DELETE FROM capabilities WHERE session_id=?",
		"DELETE FROM view_links WHERE session_id=?",
	} {
		if _, err := tx.ExecContext(ctx, query, session); err != nil {
			return err
		}
	}

	for i := len(transferTables) - 1; i >= 0; i-- {
		spec := transferTables[i]
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+spec.name+" WHERE "+spec.where, session); err != nil {
			return err
		}
	}

	return nil
}

func (s *Store) ChangeTransfer(ctx context.Context, token, action string, r TransferChangeRequest) (TransferStatus, error) {
	var result TransferStatus
	if !validToken(r.TransferID) {
		return result, failure("invalid_input", "transfer ID required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, storageError(err)
	}
	defer tx.Rollback()

	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return result, err
	}

	if err = sessionOperator(p, r.SessionID); err != nil {
		return result, err
	}

	result, err = transferStatus(ctx, tx, r.TransferID)
	if err != nil {
		return result, err
	}

	if result.Context != r.Context {
		return result, failure("forbidden", "transfer belongs to another Session")
	}

	var wasClosed bool
	if err = tx.QueryRowContext(ctx, "SELECT closed FROM sessions WHERE id=?", r.SessionID).Scan(&wasClosed); err != nil {
		return result, storageError(err)
	}

	switch action {
	case "status":
		return result, nil
	case "seal":
		if result.State == "sealed" {
			if result.DatabaseID != r.DestinationDatabase || result.Remote != r.Remote {
				return result, failure("conflict", "transfer destination changed")
			}

			return result, nil
		}

		if result.State != "prepared" || !validToken(r.DestinationDatabase) || r.DestinationDatabase == s.id {
			return result, failure("conflict", "transfer is not prepared")
		}

		var revision int64
		var payload string
		if err = tx.QueryRowContext(ctx, "SELECT read_revision,payload FROM session_transfers WHERE id=?", r.TransferID).Scan(&revision, &payload); err != nil {
			return result, storageError(err)
		}

		var session Session

		session, err = ReadSession(ctx, tx, p, r.Context)
		if err != nil {
			return result, err
		}

		if session.ReadRevision != revision {
			return result, failure("conflict", "Session changed after export; start a new transfer")
		}

		// View grants and receipt retention can change exported history without
		// changing read_revision. Check the retained rows inside the same lock.
		var current TransferBundle
		if json.Unmarshal([]byte(payload), &current) != nil {
			return result, storageError()
		}

		current.Session = session

		current.Orders, err = loadOrders(ctx, tx, session.WorkspaceID)
		if err != nil {
			return result, err
		}

		current.Tables = nil
		for _, spec := range transferTables {
			_, rows, readErr := transferRows(ctx, tx, spec, session.ID)
			if readErr != nil {
				return result, storageError(readErr)
			}

			current.Tables = append(current.Tables, TransferTable{Name: spec.name, Rows: rows})
		}

		encoded, encodeErr := json.Marshal(current)
		if encodeErr != nil {
			return result, storageError(encodeErr)
		}

		if string(encoded) != payload {
			return result, failure("conflict", "Session changed after export; start a new transfer")
		}

		_, err = tx.ExecContext(ctx, "INSERT INTO session_locations(session_id,transfer_id,state,peer_database,remote) VALUES(?,?,'sealed',?,?) ON CONFLICT(session_id) DO UPDATE SET transfer_id=excluded.transfer_id,state=excluded.state,peer_database=excluded.peer_database,remote=excluded.remote", session.ID, r.TransferID, r.DestinationDatabase, r.Remote)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE session_transfers SET state='sealed',peer_database=?,remote=?,payload='' WHERE id=?", r.DestinationDatabase, r.Remote, r.TransferID)
		}

		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE sessions SET closed=1 WHERE id=?", r.SessionID)
		}
	case "activate":
		if err = operator(p); err != nil {
			return result, err
		}

		if result.State == "active" {
			return result, nil
		}

		if result.State != "staged" {
			return result, failure("conflict", "transfer is not staged")
		}

		var current bool
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM session_locations WHERE session_id=? AND transfer_id=? AND state='staged')", r.SessionID, r.TransferID).Scan(&current); err != nil {
			return result, storageError(err)
		}

		if !current {
			return result, failure("conflict", "transfer has been superseded")
		}

		_, err = tx.ExecContext(ctx, "UPDATE sessions SET closed=(SELECT was_closed FROM session_transfers WHERE id=?) WHERE id=?", r.TransferID, r.SessionID)
		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE session_transfers SET state='active' WHERE id=?", r.TransferID)
		}

		if err == nil {
			_, err = tx.ExecContext(ctx, "UPDATE session_locations SET state='active' WHERE session_id=? AND transfer_id=?", result.SessionID, r.TransferID)
		}
	default:
		return result, failure("invalid_input", "unknown transfer action")
	}

	if err != nil {
		return result, storageError(err)
	}

	var claimsChanged bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM task_claims c JOIN tasks t ON t.id=c.task_id JOIN sessions s ON s.id=t.session_id WHERE s.id=? AND s.closed<>?)", r.SessionID, wasClosed).Scan(&claimsChanged); err != nil {
		return result, storageError(err)
	}

	if claimsChanged {
		if err = bumpWorkspaceClaims(ctx, tx, r.WorkspaceID); err != nil {
			return result, storageError(err)
		}
	}

	result, err = transferStatus(ctx, tx, r.TransferID)
	if err == nil {
		err = tx.Commit()
	}

	return result, storageOrNil(err)
}

func sessionTransferGuard(ctx context.Context, tx *sql.Tx, session string) error {
	var state string
	err := tx.QueryRowContext(ctx, "SELECT state FROM session_locations WHERE session_id=?", session).Scan(&state)
	if err == sql.ErrNoRows || err == nil && state == "active" {
		return nil
	}

	if err != nil {
		return storageError(err)
	}

	return failure("conflict", fmt.Sprintf("Session is %s for transfer; use the linked service or resume the transfer, never fall back locally", state))
}
