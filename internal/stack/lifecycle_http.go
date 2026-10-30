package stack

import (
	"context"
	"net/http"
)

func listBounds(r *ListRequest) error {
	if r.Limit == 0 {
		r.Limit = 50
	}
	if r.Limit < 1 || r.Limit > 100 || (r.After != "" && !validToken(r.After)) {
		return failure("invalid_input", "list limit must be 1–100 and after must be an opaque ID")
	}
	return nil
}

// ListLifecycle uses a single transaction for reauthentication and scoped reads.
func (s *Store) ListLifecycle(ctx context.Context, token, action string, r ListRequest) (any, error) {
	if err := listBounds(&r); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError(err)
	}
	defer tx.Rollback()
	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return nil, err
	}
	if p.Kind == BrowserCapability {
		return nil, failure("forbidden", "browser views use Session snapshot endpoints")
	}
	if p.Kind == OperatorCapability && p.SessionID != "" {
		session, err := loadSession(ctx, tx, p.SessionID)
		if err != nil {
			return nil, err
		}
		if (r.SessionID != "" && r.SessionID != session.ID) || (r.WorkspaceID != "" && r.WorkspaceID != session.WorkspaceID) {
			return nil, failure("forbidden", "credential is scoped to another Session")
		}
		r.Context = Context{WorkspaceID: session.WorkspaceID, SessionID: session.ID}
	}
	if p.Kind != OperatorCapability || r.SessionID != "" {
		if _, err = ReadSession(ctx, tx, p, r.Context); err != nil {
			return nil, err
		}
	}
	if action != "workspace.list" && !validToken(r.WorkspaceID) {
		return nil, failure("invalid_input", "explicit Workspace ID required")
	}
	if r.WorkspaceID != "" {
		if !validToken(r.WorkspaceID) {
			return nil, failure("invalid_input", "invalid Workspace ID")
		}
		var exists int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM workspaces WHERE id=?", r.WorkspaceID).Scan(&exists); err != nil {
			return nil, storageError(err)
		}
		if exists != 1 {
			return nil, failure("not_found", "Workspace not found")
		}
	}
	switch action {
	case "workspace.list":
		rows, err := tx.QueryContext(ctx, "SELECT id,path,created_at FROM workspaces WHERE (?='' OR id=?) AND id>? ORDER BY id LIMIT ?", r.WorkspaceID, r.WorkspaceID, r.After, r.Limit+1)
		if err != nil {
			return nil, storageError(err)
		}
		defer rows.Close()
		result := Page[Workspace]{Items: []Workspace{}}
		for rows.Next() {
			var w Workspace
			if err = rows.Scan(&w.ID, &w.Path, &w.CreatedAt); err != nil {
				return nil, storageError(err)
			}
			if len(result.Items) == r.Limit {
				result.Next = result.Items[len(result.Items)-1].ID
				break
			}
			result.Items = append(result.Items, w)
		}
		if err = rows.Err(); err != nil {
			return nil, storageError(err)
		}
		return result, nil
	case "session.list":
		ids, err := readIDs(ctx, tx, "SELECT id FROM sessions WHERE workspace_id=? AND (?='' OR id=?) AND id>? ORDER BY id LIMIT ?", r.WorkspaceID, r.SessionID, r.SessionID, r.After, r.Limit+1)
		if err != nil {
			return nil, err
		}
		result := Page[Session]{Items: []Session{}}
		for _, id := range ids {
			if len(result.Items) == r.Limit {
				result.Next = result.Items[len(result.Items)-1].ID
				break
			}
			session, err := loadSession(ctx, tx, id)
			if err != nil {
				return nil, err
			}
			result.Items = append(result.Items, session)
		}
		return result, nil
	case "agent.list":
		if _, err = ReadSession(ctx, tx, p, r.Context); err != nil {
			return nil, err
		}
		ids, err := readIDs(ctx, tx, "SELECT id FROM agents WHERE session_id=? AND id>? ORDER BY id LIMIT ?", r.SessionID, r.After, r.Limit+1)
		if err != nil {
			return nil, err
		}
		result := Page[Agent]{Items: []Agent{}}
		for _, id := range ids {
			if len(result.Items) == r.Limit {
				result.Next = result.Items[len(result.Items)-1].ID
				break
			}
			agent, err := agentInSession(ctx, tx, id, r.SessionID)
			if err != nil {
				return nil, err
			}
			result.Items = append(result.Items, agent)
		}
		return result, nil
	default:
		return nil, failure("not_found", "unknown list endpoint")
	}
}
func (s *Store) InspectSession(ctx context.Context, token string, ref Context) (Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, storageError(err)
	}
	defer tx.Rollback()
	p, err := authenticate(ctx, tx, token)
	if err != nil {
		return Session{}, err
	}
	if p.Kind == BrowserCapability {
		return Session{}, failure("forbidden", "browser views use Session snapshot endpoints")
	}
	session, err := ReadSession(ctx, tx, p, ref)
	if err != nil {
		return session, err
	}
	if p.Kind == AgentCapability {
		session.YourAgentID = p.SubjectID
	}
	session.OrchestratorWarning, err = s.orchestratorWarning(ctx, tx, session)
	if err != nil {
		return session, err
	}
	session.StandingOrders, err = sessionOrders(ctx, tx, session.WorkspaceID)
	if err == nil {
		session.Claims, err = sessionClaims(ctx, tx, session.ID)
	}
	return session, err
}

// CredentialContext is what the service stores for a credential: its kind, and
// for an Agent its Workspace, Session and Agent. These IDs are not secret.
type CredentialContext struct {
	Kind        CapabilityKind `json:"kind"`
	WorkspaceID string         `json:"workspace_id,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	AgentID     string         `json:"agent_id,omitempty"`
	AgentName   string         `json:"agent_name,omitempty"`
}

// ReadCredentialContext returns the caller's own context. The CLI uses it for
// a credential file from before v0.1.5, which does not name its Session.
func (s *Store) ReadCredentialContext(ctx context.Context, token string) (CredentialContext, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CredentialContext{}, storageError(err)
	}
	defer tx.Rollback()
	p, err := authenticate(ctx, tx, token)
	if err != nil || p.Kind != AgentCapability {
		return CredentialContext{Kind: p.Kind}, err
	}
	agent, err := agentInSession(ctx, tx, p.SubjectID, p.SessionID)
	if err != nil {
		return CredentialContext{}, err
	}
	return CredentialContext{Kind: p.Kind, WorkspaceID: agent.WorkspaceID, SessionID: agent.SessionID, AgentID: agent.ID, AgentName: agent.Name}, nil
}
func (s *Service) lifecycleHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.URL.Path == "/v1/credential" {
		if r.Method != http.MethodGet {
			writeError(w, failure("invalid_input", "GET required"))
			return true
		}
		data, err := s.Store.ReadCredentialContext(r.Context(), token)
		if err != nil {
			writeError(w, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
		}
		return true
	}
	var request any
	switch r.URL.Path {
	case "/v1/workspace/create":
		request = &WorkspaceCreateRequest{}
	case "/v1/workspace/list", "/v1/session/list", "/v1/agent/list":
		request = &ListRequest{}
	case "/v1/session/create":
		request = &SessionCreateRequest{}
	case "/v1/session/inspect":
		request = &Context{}
	case "/v1/session/close", "/v1/session/reopen", "/v1/session/appoint":
		request = &SessionChangeRequest{}
	case "/v1/workspace/orders/show":
		request = &ordersRead{}
	case "/v1/workspace/orders/set", "/v1/workspace/orders/clear":
		request = &OrdersRequest{}
	case "/v1/agent/register":
		request = &AgentRegisterRequest{}
	case "/v1/agent/attach", "/v1/agent/rebind":
		request = &AgentBindingRequest{}
	case "/v1/agent/observe":
		request = &AgentObserveRequest{}
	default:
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	if err := requestJSON(w, r, request); err != nil {
		writeError(w, err)
		return true
	}
	var data any
	var err error
	switch request := request.(type) {
	case *WorkspaceCreateRequest:
		data, err = s.Store.CreateWorkspace(r.Context(), token, *request)
	case *SessionCreateRequest:
		data, err = s.Store.CreateSession(r.Context(), token, *request)
	case *Context:
		data, err = s.Store.InspectSession(r.Context(), token, *request)
	case *ordersRead:
		data, err = s.Store.ReadOrders(r.Context(), token, Context(*request))
	case *OrdersRequest:
		request.Clear = r.URL.Path == "/v1/workspace/orders/clear"
		data, err = s.Store.ChangeOrders(r.Context(), token, *request)
	case *ListRequest:
		action := "workspace.list"
		if r.URL.Path == "/v1/session/list" {
			action = "session.list"
		}
		if r.URL.Path == "/v1/agent/list" {
			action = "agent.list"
		}
		data, err = s.Store.ListLifecycle(r.Context(), token, action, *request)
	case *SessionChangeRequest:
		action := "close"
		if r.URL.Path == "/v1/session/reopen" {
			action = "reopen"
		}
		if r.URL.Path == "/v1/session/appoint" {
			action = "appoint"
		}
		data, err = s.Store.ChangeSession(r.Context(), token, action, *request)
	case *AgentRegisterRequest:
		data, err = s.Store.RegisterAgent(r.Context(), token, *request)
	case *AgentBindingRequest:
		action := "attach"
		if r.URL.Path == "/v1/agent/rebind" {
			action = "rebind"
		}
		data, err = s.Store.BindAgent(r.Context(), token, action, *request)
	case *AgentObserveRequest:
		data, err = s.Store.ObserveAgent(r.Context(), token, *request)
	}
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
