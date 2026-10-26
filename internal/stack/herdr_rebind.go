package stack

import (
	"context"
	"encoding/json"
	"net/http"
	"unicode/utf8"
)

func liveBindingConflict(name string) error {
	return failure("conflict", "Agent "+name+" already has a binding; the Operator must run agent rebind --clear-runtime with a new credential file, then the Agent must bind again")
}

// stale needs positive metadata evidence, not a timeout, a changed alias or
// an idle/blocked/stopped observation. Look up the recorded pane, not its alias.
func (h *herdrAdapter) stale(ctx context.Context, b RuntimeBinding) bool {
	if !completeHerdrBinding(b) || h.compatible(ctx, b) != "" {
		return false
	}
	out, diagnostic, _, err := h.command(ctx, b, "agent", "get", b.PaneID)
	var response herdrResponse
	if err != nil {
		return utf8.Valid(diagnostic) && json.Unmarshal(diagnostic, &response) == nil && (response.Error.Code == "not_found" || response.Error.Code == "agent_not_found")
	}
	if !utf8.Valid(out) || json.Unmarshal(out, &response) != nil || response.Error.Code != "" {
		return false
	}
	a := response.Result.Agent
	if a.PaneID != b.PaneID || !safeBindingField(a.TerminalID) {
		return false
	}
	if a.TerminalID != b.TerminalID {
		return true
	}
	return bindableHarnesses[a.Agent] && safeBindingField(a.Session.Value) && nativeSession(a.Agent, a.Session.Agent, a.Session.Kind, a.Session.Source, a.Session.Value) && (a.Agent != b.Harness || a.Session.Value != b.NativeSessionID)
}

type herdrBindCheck struct {
	replace bool
	err     error
}

// Prepare outside the serialized connection. The mutation rechecks identity,
// openness and revision before it uses the result; a replay ignores it.
func (s *Store) prepareHerdrBind(ctx context.Context, token string, r AgentBindingRequest, h *herdrAdapter) (bool, error) {
	tx, p, session, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if p.Kind != AgentCapability || p.SubjectID != r.AgentID {
		return false, failure("forbidden", "only that Agent can bind its own Herdr pane")
	}
	if session.Closed {
		return false, failure("session_closed", "Session is closed")
	}
	agent, err := agentInSession(ctx, tx, r.AgentID, session.ID)
	if err != nil {
		return false, err
	}
	if err = expectedRevision(r.ExpectedRevision, agent.Revision); err != nil {
		return false, err
	}
	var encoded string
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(binding,'') FROM agents WHERE id=?", agent.ID).Scan(&encoded); err != nil {
		return false, storageError()
	}
	tx.Rollback()
	if r.Runtime == nil || !completeHerdrBinding(*r.Runtime) || r.Runtime.InstanceID != "herdr:"+r.Runtime.TerminalID {
		return false, failure("invalid_input", "complete Herdr binding required")
	}
	if _, reason := h.current(ctx, *r.Runtime); reason != "" {
		return false, failure("integration_unavailable", "the new Herdr pane could not be verified: "+reason)
	}
	if !agent.RuntimeAttached {
		return false, nil
	}
	var old RuntimeBinding
	if json.Unmarshal([]byte(encoded), &old) != nil || !h.stale(ctx, old) {
		return false, liveBindingConflict(agent.Name)
	}
	return true, nil
}

func (s *Store) bindHerdrAgent(ctx context.Context, token string, r AgentBindingRequest, h *herdrAdapter) (Receipt, error) {
	if h == nil {
		return s.BindAgent(ctx, token, "attach", r)
	}
	check := &herdrBindCheck{}
	check.replace, check.err = s.prepareHerdrBind(ctx, token, r, h)
	return s.bindAgent(ctx, token, "attach", r, check)
}

func (s *Service) herdrBindHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.URL.Path != "/v1/herdr/bind" {
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	var q AgentBindingRequest
	if err := requestJSON(w, r, &q); err != nil {
		writeError(w, err)
		return true
	}
	var adapter *herdrAdapter
	if s.notifier != nil {
		adapter = s.notifier.adapter
	}
	// Without a configured adapter a fresh attach still works, but an
	// established binding cannot be declared stale by the calling client.
	data, err := s.Store.bindHerdrAgent(r.Context(), token, q, adapter)
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
