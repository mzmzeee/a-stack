package stack

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const migration14 = `
ALTER TABLE mail_messages ADD COLUMN priority TEXT NOT NULL DEFAULT 'normal' CHECK(priority IN ('normal','urgent'));
CREATE INDEX mail_messages_urgent ON mail_messages(task_id,sequence) WHERE priority='urgent';
`

// UrgentMailPosition covers a batch of urgent messages and due ACK reminders.
// It is a hook-local high-water mark, never an Agent read position or an ACK.
type UrgentMailPosition struct {
	Sequence int64  `json:"sequence"`
	Due      string `json:"due"`
	DueID    string `json:"due_id"`
}
type UrgentMailRequest struct {
	Context
	Positions map[string]UrgentMailPosition `json:"positions,omitempty"`
	AfterTask string                        `json:"after_task,omitempty"`
}
type UrgentMailPage struct {
	Positions map[string]UrgentMailPosition `json:"positions"`
	NextTask  string                        `json:"next_task,omitempty"`
	Urgent    bool                          `json:"urgent"`
}

// PeekUrgentMail returns bounded, body-free references only. It neither re-arms
// terminal notices nor changes acknowledgements. The caller serializes its cache.
func (s *Store) PeekUrgentMail(ctx context.Context, token string, r UrgentMailRequest) (UrgentMailPage, error) {
	out := UrgentMailPage{Positions: map[string]UrgentMailPosition{}}
	tx, p, session, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if p.Kind != AgentCapability {
		return out, failure("forbidden", "urgent mail peek requires an Agent credential")
	}
	if session.Closed {
		return out, nil
	}
	after, err := decodeMailCheckCursor(r.SessionID, p.SubjectID, r.AfterTask)
	if err != nil {
		return out, err
	}
	for task, pos := range r.Positions {
		if !ValidID(task) || pos.Sequence < 0 || len(pos.Due) > 35 || (pos.DueID != "" && !ValidID(pos.DueID)) {
			return out, failure("invalid_input", "invalid urgent mail position")
		}
	}
	ids, err := readIDs(ctx, tx, "SELECT task_id FROM task_assignees WHERE session_id=? AND agent_id=? AND task_id>? ORDER BY task_id LIMIT 101", r.SessionID, p.SubjectID, after)
	if err != nil {
		return out, publicError(err)
	}
	if len(ids) > 100 {
		ids = ids[:100]
		out.NextTask = encodeMailCheckCursor(r.SessionID, p.SubjectID, ids[99])
	}
	for _, task := range ids {
		pos := r.Positions[task]
		var sequence int64
		// A new assignment restores history, not old wake attempts. No mention is
		// required: as with normal hook nudges, all current assignees can see mail.
		err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(m.sequence),0) FROM mail_messages m JOIN task_assignees x ON x.task_id=m.task_id WHERE m.task_id=? AND x.agent_id=? AND m.priority='urgent' AND COALESCE(m.sender_id,'')<>? AND rtrim(m.received_at,'Z')>=rtrim(x.added_at,'Z')`, task, p.SubjectID, p.SubjectID).Scan(&sequence)
		if err != nil {
			return out, publicError(err)
		}
		if sequence > pos.Sequence {
			pos.Sequence = sequence
			out.Urgent = true
		}
		var due string
		var id string
		err = tx.QueryRowContext(ctx, `SELECT rtrim(due_at,'Z'),message_id FROM mail_acks WHERE task_id=? AND recipient_id=? AND acked_at='' AND withdrawn_at='' AND reminder_state<>'suppressed' AND due_at<>'' AND rtrim(due_at,'Z')<=rtrim(?,'Z') AND (rtrim(due_at,'Z')>? OR (rtrim(due_at,'Z')=? AND message_id>?)) ORDER BY rtrim(due_at,'Z') DESC,message_id DESC LIMIT 1`, task, p.SubjectID, clockNow().UTC().Format(time.RFC3339Nano), pos.Due, pos.Due, pos.DueID).Scan(&due, &id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return out, publicError(err)
		}
		if err == nil {
			pos.Due = due
			pos.DueID = id
			out.Urgent = true
		}
		out.Positions[task] = pos
	}
	return out, nil
}
