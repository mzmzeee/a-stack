package stack

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// MaxOrdersBytes bounds the standing orders of a Workspace. They go into
// every Agent's reminder, so they stay short.
const MaxOrdersBytes = 4096

// StandingOrders are the Operator's rules for every Agent of a Workspace.
type StandingOrders struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Body        string `json:"body"`
	Revision    int64  `json:"revision"`
	UpdatedAt   string `json:"updated_at,omitempty"`
}

// ordersRead is the body of workspace/orders/show; the type keeps it apart
// from session inspect in the HTTP switch.
type ordersRead Context

// OrdersRequest sets the standing orders, or clears them with Clear.
type OrdersRequest struct {
	OperationID      string `json:"operation_id"`
	WorkspaceID      string `json:"workspace_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	Body             string `json:"body,omitempty"`
	Clear            bool   `json:"-"` // from the path
}

// loadOrders reads the standing orders of a Workspace inside tx.
func loadOrders(ctx context.Context, tx *sql.Tx, workspace string) (StandingOrders, error) {
	o := StandingOrders{WorkspaceID: workspace, Revision: 1}
	err := tx.QueryRowContext(ctx, "SELECT body,revision,updated_at FROM workspace_orders WHERE workspace_id=?", workspace).Scan(&o.Body, &o.Revision, &o.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return o, storageError(err)
	}
	return o, nil
}

// sessionOrders is the orders of a Session's Workspace for session reads, or
// nil when there are none.
func sessionOrders(ctx context.Context, tx *sql.Tx, workspace string) (*StandingOrders, error) {
	o, err := loadOrders(ctx, tx, workspace)
	if err != nil || o.Body == "" {
		return nil, err
	}
	o.WorkspaceID = ""
	return &o, nil
}

// validOrders allows plain text: UTF-8 without control characters other
// than tab and line breaks.
func validOrders(body string) error {
	if err := validText("standing orders", body, MaxOrdersBytes, true); err != nil {
		return err
	}
	if strings.ContainsFunc(body, func(r rune) bool { return (r < 0x20 && r != '\t' && r != '\n' && r != '\r') || r == 0x7f }) {
		return failure("invalid_input", "standing orders must be plain text without control characters")
	}
	return nil
}

// ReadOrders shows the standing orders of a Workspace: the Operator's for any
// Workspace, an Agent's for its own. The browser view reads them in its
// Session snapshot.
func (s *Store) ReadOrders(ctx context.Context, token string, ref Context) (StandingOrders, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StandingOrders{}, storageError(err)
	}
	defer tx.Rollback()
	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return StandingOrders{}, err
	}
	switch p.Kind {
	case OperatorCapability:
		if err = workspaceExists(ctx, tx, ref.WorkspaceID); err != nil {
			return StandingOrders{}, err
		}
	case AgentCapability:
		if _, err = ReadSession(ctx, tx, p, ref); err != nil {
			return StandingOrders{}, err
		}
	default:
		return StandingOrders{}, failure("forbidden", "browser views read standing orders in the Session snapshot")
	}
	return loadOrders(ctx, tx, ref.WorkspaceID)
}

func workspaceExists(ctx context.Context, tx *sql.Tx, id string) error {
	if !validToken(id) {
		return failure("invalid_input", "explicit Workspace ID required")
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM workspaces WHERE id=?", id).Scan(&n); err != nil {
		return storageError(err)
	}
	if n != 1 {
		return failure("not_found", "Workspace not found")
	}
	return nil
}

// ChangeOrders sets or clears the standing orders of a Workspace. Only the
// Operator writes them. Every Session of the Workspace gets a new read
// revision, so open views show the change.
func (s *Store) ChangeOrders(ctx context.Context, token string, r OrdersRequest) (Receipt, error) {
	name := "workspace.orders.set"
	if r.Clear {
		name = "workspace.orders.clear"
	}
	replay := func(ctx context.Context, tx *sql.Tx, p Principal, _ Receipt) error { return operator(p) }
	return s.Mutate(ctx, token, operation(r.OperationID, name, r.WorkspaceID, r), replay, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if err := operator(p); err != nil {
			return Receipt{}, err
		}
		if r.Clear && r.Body != "" {
			return Receipt{}, failure("invalid_input", "clear takes no body")
		}
		if !r.Clear {
			if err := validOrders(r.Body); err != nil {
				return Receipt{}, err
			}
		}
		if err := workspaceExists(ctx, tx, r.WorkspaceID); err != nil {
			return Receipt{}, err
		}
		current, err := loadOrders(ctx, tx, r.WorkspaceID)
		if err != nil {
			return Receipt{}, err
		}
		if err = expectedRevision(r.ExpectedRevision, current.Revision); err != nil {
			return Receipt{}, err
		}
		outcome := "set"
		if r.Clear {
			outcome = "cleared"
		}
		if current.Body == r.Body {
			return Receipt{ResourceID: r.WorkspaceID, Revision: current.Revision, Outcome: outcome}, nil
		}
		next := current.Revision + 1
		if _, err = tx.ExecContext(ctx, "INSERT INTO workspace_orders VALUES(?,?,?,?) ON CONFLICT(workspace_id) DO UPDATE SET body=excluded.body,revision=excluded.revision,updated_at=excluded.updated_at", r.WorkspaceID, r.Body, next, now()); err != nil {
			return Receipt{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE sessions SET read_revision=read_revision+1 WHERE workspace_id=?", r.WorkspaceID); err != nil {
			return Receipt{}, err
		}
		if err = recordLifecycle(ctx, tx, p, name, r.WorkspaceID, "", next); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: r.WorkspaceID, Revision: next, Outcome: outcome}, nil
	})
}
