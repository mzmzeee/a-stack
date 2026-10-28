package stack

import (
	"context"
	"database/sql"
	"encoding/json"
)

// NoticeWarning is bounded public guidance, never runtime identity or raw
// Herdr output. Reads use retained outcomes, the binding and the last diagnostic.
type NoticeWarning struct {
	Reason              string `json:"reason"`
	Fix                 string `json:"fix"`
	UnresolvedUncertain int    `json:"unresolved_uncertain,omitempty"`
}

func noticeWarning(reason string) *NoticeWarning {
	fix := ""
	switch reason {
	case "notice_waiting_mail":
		fix = "Notice waiting until the Agent reads its mail, or the 10-minute cooldown passes."
	case "notice_waiting_idle":
		fix = "Notice waiting until the Agent is idle, or the 10-minute cooldown passes; its hooks can nudge it between tool calls."
	case "delivery_uncertain", "recovery_uncertain":
		fix = "Some notices may have been sent; check retained Task mail. Uncertain notices are not resent automatically."
	case "binding_incomplete":
		fix = "Run herdr bind in your harness pane; if integration_missing, install the named Herdr integration and restart the harness in that pane."
	case "runtime_unavailable", "identity_changed":
		fix = "Check that your harness is running in its bound pane; in a new pane or session, run herdr bind again. A live binding needs the Operator's agent rebind --clear-runtime first."
	case "unsupported_harness":
		fix = "Use a supported harness version and bind again; until then, check mail normally."
	case "integration_disabled":
		fix = "Ask the Operator to enable the service's Herdr adapter with --herdr-executable; check mail normally."
	case "herdr_unavailable", "unsupported_herdr":
		fix = "Ask the Operator to check the Herdr server and supported protocol; check mail normally."
	case "runtime_blocked":
		fix = "Resolve the harness dialog in its pane; notices retry without answering it for you."
	}
	if fix == "" {
		return nil
	}
	return &NoticeWarning{Reason: reason, Fix: fix}
}

func (s *Store) noticeWarningTx(ctx context.Context, tx *sql.Tx, session, agent string) (*NoticeWarning, error) {
	var encoded, reason string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(a.binding,''),COALESCE(d.reason,'') FROM agents a
 LEFT JOIN notice_targets d ON d.session_id=a.session_id AND d.recipient_id=a.id WHERE a.session_id=? AND a.id=?`, session, agent).Scan(&encoded, &reason); err != nil {
		return nil, storageError()
	}
	var uncertain int
	if err := tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM mail_mentions WHERE recipient_id=?1 AND notice_state='uncertain')+(SELECT count(*) FROM mail_acks WHERE recipient_id=?1 AND reminder_state='uncertain' AND acked_at='' AND withdrawn_at='')", agent).Scan(&uncertain); err != nil {
		return nil, storageError()
	}
	if uncertain > 0 {
		if reason != "recovery_uncertain" {
			reason = "delivery_uncertain"
		}
		warning := noticeWarning(reason)
		warning.UnresolvedUncertain = uncertain
		return warning, nil
	}
	var binding RuntimeBinding
	_ = json.Unmarshal([]byte(encoded), &binding)
	if !completeHerdrBinding(binding) {
		reason = "binding_incomplete"
	} else if !verifiedHarness(binding) {
		reason = "unsupported_harness"
	}
	if warning := noticeWarning(reason); warning != nil {
		return warning, nil
	}
	if s.noticeWake == nil {
		return noticeWarning("integration_disabled"), nil
	}
	return nil, nil
}
