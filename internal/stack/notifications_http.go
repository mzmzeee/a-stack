package stack

import (
	"context"
	"net/http"
)

type NoticeRequest struct {
	Context
	AgentID string `json:"agent_id,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	After   string `json:"after,omitempty"`
}
type NoticeTargetStatus struct {
	AgentID     string              `json:"agent_id"`
	AgentName   string              `json:"agent_name"`
	Reason      string              `json:"reason"`
	NextAttempt int64               `json:"next_attempt_unix_ms"`
	UpdatedAt   string              `json:"updated_at,omitempty"`
	Counts      map[NoticeState]int `json:"counts"`
}
type NoticeStatus struct {
	Enabled bool                 `json:"enabled"`
	Targets []NoticeTargetStatus `json:"targets"`
	Next    string               `json:"next,omitempty"`
}

func (s *Service) notificationStatus(ctx context.Context, token string, r NoticeRequest) (NoticeStatus, error) {
	result := NoticeStatus{Enabled: s.notifier != nil, Targets: []NoticeTargetStatus{}}
	if r.Limit == 0 {
		r.Limit = 50
	}
	if r.Limit < 1 || r.Limit > 100 || (r.After != "" && !validToken(r.After)) {
		return result, failure("invalid_input", "invalid notification status page")
	}
	tx, p, session, err := s.Store.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if p.Kind == BrowserCapability {
		return result, failure("forbidden", "browser credentials cannot inspect delivery state")
	}
	if p.Kind == AgentCapability {
		if r.AgentID != "" && r.AgentID != p.SubjectID {
			return result, failure("forbidden", "Agent delivery state is private to that Agent")
		}
		r.AgentID = p.SubjectID
	}
	if r.AgentID != "" {
		if _, err = agentInSession(ctx, tx, r.AgentID, session.ID); err != nil {
			return result, err
		}
	}
	ids, err := readIDs(ctx, tx, "SELECT id FROM agents WHERE session_id=? AND id>? AND (?='' OR id=?) ORDER BY id LIMIT ?", session.ID, r.After, r.AgentID, r.AgentID, r.Limit+1)
	if err != nil {
		return result, err
	}
	for _, id := range ids {
		if len(result.Targets) == r.Limit {
			result.Next = result.Targets[len(result.Targets)-1].AgentID
			break
		}
		row := NoticeTargetStatus{AgentID: id, Counts: map[NoticeState]int{}}
		if err = tx.QueryRowContext(ctx, "SELECT a.name,COALESCE(d.reason,'pending'),COALESCE(d.next_attempt,0),COALESCE(d.updated_at,'') FROM agents a LEFT JOIN notice_targets d ON d.recipient_id=a.id AND d.session_id=a.session_id WHERE a.id=?", id).Scan(&row.AgentName, &row.Reason, &row.NextAttempt, &row.UpdatedAt); err != nil {
			return result, storageError()
		}
		// Enumerate the bounded states so the existing state/recipient index does
		// point ranges instead of scanning every recipient's retained history.
		counts, err := tx.QueryContext(ctx, "SELECT notice_state,count(*) FROM mail_mentions WHERE notice_state IN ('pending','submitting','submitted','uncertain','suppressed','self') AND recipient_id=? GROUP BY notice_state", id)
		if err != nil {
			return result, storageError()
		}
		for counts.Next() {
			var state NoticeState
			var count int
			if err = counts.Scan(&state, &count); err != nil {
				counts.Close()
				return result, storageError()
			}
			row.Counts[state] = count
		}
		err = counts.Err()
		counts.Close()
		if err != nil {
			return result, storageError()
		}
		if s.notifier == nil {
			row.Reason = "integration_disabled"
		} else if session.Closed {
			row.Reason = "session_closed"
		}
		result.Targets = append(result.Targets, row)
	}
	return result, nil
}
func (s *Service) notificationHTTP(w http.ResponseWriter, r *http.Request, token string) bool {
	if r.URL.Path != "/v1/notifications/status" && r.URL.Path != "/v1/notifications/drain" {
		return false
	}
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return true
	}
	var q NoticeRequest
	if err := requestJSON(w, r, &q); err != nil {
		writeError(w, err)
		return true
	}
	var data any
	var err error
	if r.URL.Path == "/v1/notifications/status" {
		data, err = s.notificationStatus(r.Context(), token, q)
	} else {
		if q.AgentID != "" || q.After != "" {
			err = failure("invalid_input", "drain accepts only context and limit")
		} else {
			if q.Limit == 0 {
				q.Limit = 4
			}
			if q.Limit < 1 || q.Limit > 16 {
				err = failure("invalid_input", "drain limit must be 1–16")
			} else {
				tx, p, session, e := s.Store.readSessionTx(r.Context(), token, q.Context)
				err = e
				if err == nil {
					err = operator(p)
					tx.Rollback()
				}
				if err == nil && s.notifier == nil {
					err = failure("integration_unavailable", "notifications are disabled; ordinary mail checks remain available")
				}
				if err == nil {
					data, err = s.notifier.drain(r.Context(), session.ID, q.Limit)
				}
			}
		}
	}
	if err != nil {
		writeError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	}
	return true
}
