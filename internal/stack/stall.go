package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const migration15 = `
ALTER TABLE agents ADD COLUMN last_mail_read_at TEXT;
CREATE INDEX notice_batch_refs_message ON notice_batch_refs(recipient_id,message_id);
`

const (
	MentionEscalationAfter        = 10 * time.Minute
	MailStalledAfter              = 30 * time.Minute
	OrchestratorReachabilityAfter = 15 * time.Minute
)

// MailWait describes a current mention, not an acknowledgement obligation.
type MailWait struct {
	TaskID        string `json:"task_id"`
	SenderName    string `json:"sender_name"`
	RecipientID   string `json:"recipient_id"`
	RecipientName string `json:"recipient_name"`
	Since         string `json:"since"`
	Minutes       int    `json:"minutes"`
	Stalled       bool   `json:"stalled"`
	Text          string `json:"text"`
}

// Exact successful/uncertain batch refs are the durable one-extra ledger.
// Stored times are UTC; trimming Z keeps nanoseconds (julianday rounds to milliseconds).
const escalatableMentionSQL = `m.notice_state='submitted'
 AND rtrim(m.state_at,'Z')<=rtrim(?,'Z')
 AND (a.last_mail_read_at IS NULL OR rtrim(msg.received_at,'Z')>rtrim(a.last_mail_read_at,'Z'))
 AND EXISTS(SELECT 1 FROM task_assignees x WHERE x.task_id=m.task_id AND x.agent_id=a.id AND rtrim(x.added_at,'Z')<=rtrim(msg.received_at,'Z'))
 AND (SELECT count(*) FROM notice_batch_refs r JOIN notice_batches b ON b.id=r.batch_id WHERE r.message_id=m.message_id AND r.recipient_id=m.recipient_id AND b.state IN ('submitted','uncertain','submitting'))<2`

const unreadMentionSQL = ` FROM mail_mentions m JOIN mail_messages msg ON msg.id=m.message_id
 JOIN agents a ON a.id=m.recipient_id
 JOIN task_assignees x ON x.task_id=m.task_id AND x.agent_id=m.recipient_id
 LEFT JOIN agents sender ON sender.id=msg.sender_id
 WHERE m.notice_state NOT IN ('self','suppressed')
 AND (a.last_mail_read_at IS NULL OR rtrim(msg.received_at,'Z')>rtrim(a.last_mail_read_at,'Z'))
 AND rtrim(msg.received_at,'Z')>=rtrim(x.added_at,'Z')`

func mailWaits(ctx context.Context, tx *sql.Tx, session, task, agent string) ([]MailWait, error) {
	query := `SELECT m.task_id,COALESCE(sender.name,'Operator'),a.id,a.name,MIN(rtrim(msg.received_at,'Z'))||'Z'` + unreadMentionSQL + ` AND x.session_id=?`
	args := []any{session}
	if task != "" {
		query += " AND m.task_id=?"
		args = append(args, task)
	}

	if agent != "" {
		query += " AND m.recipient_id=?"
		args = append(args, agent)
	}

	query += ` GROUP BY m.task_id,msg.sender_id,a.id ORDER BY MIN(rtrim(msg.received_at,'Z')),m.task_id,a.id LIMIT 100`

	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()

	var waits []MailWait
	for rows.Next() {
		var w MailWait
		if err = rows.Scan(&w.TaskID, &w.SenderName, &w.RecipientID, &w.RecipientName, &w.Since); err != nil {
			return nil, storageError(err)
		}

		at, err := time.Parse(time.RFC3339Nano, w.Since)
		if err != nil {
			return nil, storageError(err)
		}

		age := clockNow().Sub(at)
		w.Minutes = max(0, int(age/time.Minute))
		w.Stalled = age >= MailStalledAfter
		w.Text = fmt.Sprintf("%s has waited %d min for %s", w.SenderName, w.Minutes, w.RecipientName)
		waits = append(waits, w)
	}

	return waits, rows.Err()
}

func (s *Store) stallMark(ctx context.Context, tx *sql.Tx, session Session, at time.Time) (int64, time.Time, error) {
	var count int64
	var nextStamp string
	cutoff := at.Add(-MailStalledAfter).UTC().Format(time.RFC3339Nano)

	err := tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE rtrim(msg.received_at,'Z')<=rtrim(?1,'Z')),COALESCE((min(rtrim(msg.received_at,'Z')) FILTER (WHERE rtrim(msg.received_at,'Z')>rtrim(?1,'Z')))||'Z','')`+unreadMentionSQL+` AND x.session_id=?2`, cutoff, session.ID).Scan(&count, &nextStamp)
	if err != nil {
		return 0, time.Time{}, storageError(err)
	}

	var next time.Time
	if stamp, err := time.Parse(time.RFC3339Nano, nextStamp); err == nil {
		next = stamp.Add(MailStalledAfter)
	}

	warning, err := s.orchestratorWarning(ctx, tx, session)
	if err != nil {
		return 0, time.Time{}, err
	}

	if warning != "" {
		count++
	}

	if session.OrchestratorID != nil && warning == "" {
		var observed, read string
		if err = tx.QueryRowContext(ctx, "SELECT observed_at,COALESCE(last_mail_read_at,'') FROM agents WHERE id=?", *session.OrchestratorID).Scan(&observed, &read); err != nil {
			return 0, time.Time{}, storageError(err)
		}

		for _, stamp := range []string{observed, read} {
			if t, e := time.Parse(time.RFC3339Nano, stamp); e == nil {
				due := t.Add(OrchestratorReachabilityAfter)
				if due.After(at) && (next.IsZero() || due.Before(next)) {
					next = due
				}
			}
		}
	}

	return count, next, nil
}

func (s *Store) orchestratorWarning(ctx context.Context, tx *sql.Tx, session Session) (string, error) {
	if session.OrchestratorID == nil || session.Closed {
		return "", nil
	}

	var encoded, observed, read, reason string

	err := tx.QueryRowContext(ctx, `SELECT COALESCE(a.binding,''),a.observed_at,COALESCE(a.last_mail_read_at,''),COALESCE(d.reason,'') FROM agents a LEFT JOIN notice_targets d ON d.session_id=a.session_id AND d.recipient_id=a.id WHERE a.id=?`, *session.OrchestratorID).Scan(&encoded, &observed, &read, &reason)
	if err != nil {
		return "", storageError(err)
	}

	var binding RuntimeBinding
	_ = json.Unmarshal([]byte(encoded), &binding)

	reachable := !binding.RemoteMachine && binding.Notices == "pane" && s.noticeWake != nil && completeHerdrBinding(binding) && verifiedHarness(binding)
	switch reason {
	case "runtime_unavailable", "identity_changed", "unsupported_harness", "binding_incomplete", "herdr_unavailable", "unsupported_herdr", "runtime_blocked":
		reachable = false
	}

	if reachable {
		return "", nil
	}

	for _, stamp := range []string{observed, read} {
		at, e := time.Parse(time.RFC3339Nano, stamp)
		if e == nil && clockNow().Sub(at) < OrchestratorReachabilityAfter {
			return "", nil
		}
	}

	return "the Orchestrator cannot be reached by mail: send the blocking question as Task mail with --ack-required, set the Task Waiting naming the recipient, and use Needs you only when the Operator must unblock it.", nil
}
