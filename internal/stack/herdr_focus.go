package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type HerdrFocusRequest struct {
	Context
	// Agent is the Agent's name or ID in the Session.
	Agent string `json:"agent"`
}

// focus shows the bound pane in the Operator's Herdr after the same identity
// check as a notice. A blocked or unverified harness can still be focused.
func (h *herdrAdapter) focus(ctx context.Context, b RuntimeBinding) (string, error) {
	if !completeHerdrBinding(b) {
		return "", failure("integration_unavailable", "the Agent has no complete Herdr binding; it can run a-stack herdr bind")
	}
	agent, reason := h.current(ctx, b)
	if reason != "" {
		return "", failure("integration_unavailable", "the Agent's Herdr pane could not be verified: "+reason)
	}
	if _, ok := h.agent(ctx, b, "agent", "focus", b.PaneID); ok {
		return "agent", nil
	}
	if safeBindingField(agent.TabID) && !strings.HasPrefix(agent.TabID, "-") {
		if _, _, _, err := h.command(ctx, b, "tab", "focus", agent.TabID); err == nil {
			return "tab", nil
		}
	}
	return "", failure("integration_unavailable", "Herdr did not focus the Agent's pane")
}

// herdrFocus is Operator-only, because bindings are never shown to clients.
// The Herdr commands run after the read transaction ends.
func (s *Service) herdrFocus(ctx context.Context, token string, r HerdrFocusRequest) (map[string]string, error) {
	tx, p, session, err := s.Store.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return nil, err
	}
	var binding string
	if err = operator(p); err == nil {
		err = tx.QueryRowContext(ctx, "SELECT COALESCE(binding,'') FROM agents WHERE session_id=? AND (id=? OR name=?) ORDER BY id=? DESC LIMIT 1", session.ID, r.Agent, r.Agent, r.Agent).Scan(&binding)
		if errors.Is(err, sql.ErrNoRows) {
			err = failure("not_found", "Agent not found in this Session")
		} else if err != nil {
			err = storageError()
		}
	}
	tx.Rollback()
	if err != nil {
		return nil, err
	}
	if s.notifier == nil {
		return nil, failure("integration_unavailable", "start the service with --herdr-executable to use Herdr")
	}
	var b RuntimeBinding
	_ = json.Unmarshal([]byte(binding), &b)
	focused, err := s.notifier.adapter.focus(ctx, b)
	if err != nil {
		return nil, err
	}
	return map[string]string{"focused": focused}, nil
}
func (s *Service) herdrHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.URL.Path != "/v1/herdr/focus" {
		return false
	}
	var q HerdrFocusRequest
	var err error = failure("invalid_input", "POST JSON required")
	if r.Method == http.MethodPost {
		err = requestJSON(w, r, &q)
	}
	var data map[string]string
	if err == nil && q.Agent == "" {
		err = failure("invalid_input", "agent required")
	}
	if err == nil {
		data, err = s.herdrFocus(r.Context(), token, q)
	}
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
