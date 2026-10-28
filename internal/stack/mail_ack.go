package stack

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

const migration12 = `
CREATE TABLE mail_acks(
 message_id TEXT NOT NULL, task_id TEXT NOT NULL, recipient_id TEXT NOT NULL REFERENCES agents(id),
 due_at TEXT NOT NULL DEFAULT '', acked_at TEXT NOT NULL DEFAULT '', withdrawn_at TEXT NOT NULL DEFAULT '',
 reminder_state TEXT NOT NULL DEFAULT 'pending' CHECK(reminder_state IN ('pending','submitting','submitted','uncertain','suppressed')),
 PRIMARY KEY(message_id,recipient_id), FOREIGN KEY(message_id) REFERENCES mail_messages(id)
) WITHOUT ROWID;
CREATE TRIGGER mail_acks_task_insert BEFORE INSERT ON mail_acks
 WHEN NEW.task_id IS NOT (SELECT task_id FROM mail_messages WHERE id=NEW.message_id)
 BEGIN SELECT RAISE(ABORT,'FOREIGN KEY constraint failed'); END;
CREATE TRIGGER mail_acks_task_update BEFORE UPDATE OF message_id,task_id ON mail_acks
 WHEN NEW.task_id IS NOT (SELECT task_id FROM mail_messages WHERE id=NEW.message_id)
 BEGIN SELECT RAISE(ABORT,'FOREIGN KEY constraint failed'); END;
CREATE INDEX mail_acks_recipient ON mail_acks(recipient_id,message_id) WHERE acked_at='' AND withdrawn_at='';
CREATE INDEX mail_acks_task ON mail_acks(task_id,message_id,recipient_id) WHERE acked_at='' AND withdrawn_at='';
CREATE INDEX mail_acks_due ON mail_acks(recipient_id,due_at,message_id) WHERE reminder_state='pending' AND acked_at='' AND withdrawn_at='' AND due_at<>'';
CREATE TABLE notice_ack_refs(
 batch_id TEXT NOT NULL REFERENCES notice_batches(id), message_id TEXT NOT NULL, recipient_id TEXT NOT NULL,
 PRIMARY KEY(batch_id,message_id), FOREIGN KEY(message_id,recipient_id) REFERENCES mail_acks(message_id,recipient_id)
) WITHOUT ROWID;
`

type MailAck struct {
	MessageID     string `json:"message_id"`
	TaskID        string `json:"task_id"`
	RecipientID   string `json:"recipient_id"`
	RecipientName string `json:"recipient_name"`
	DueAt         string `json:"due_at,omitempty"`
	AckedAt       string `json:"acked_at,omitempty"`
	WithdrawnAt   string `json:"withdrawn_at,omitempty"`
}

type MailAckRequest struct {
	Context
	OperationID string `json:"operation_id"`
	MessageID   string `json:"message_id"`
}

func appendMailAcks(ctx context.Context, tx *sql.Tx, r MailSendRequest, message string, mentions []string) error {
	if !r.AckRequired {
		if len(r.AckFrom) > 0 || r.AckDue != "" {
			return failure("invalid_input", "ack-from and ack-due require ack-required")
		}
		return nil
	}
	recipients := mentions
	if len(r.AckFrom) > 0 {
		names, err := mentionNames("ack", r.AckFrom)
		if err != nil {
			return err
		}
		recipients, err = resolveMentions(ctx, tx, r.TaskID, names)
		if err != nil {
			return err
		}
	}
	if len(recipients) == 0 {
		return failure("invalid_input", "ack-required needs at least one mentioned or selected current assignee")
	}
	due := ""
	if r.AckDue != "" {
		d, err := time.ParseDuration(r.AckDue)
		if err != nil || d <= 0 {
			return failure("invalid_input", "ack-due must be a positive duration")
		}
		due = clockNow().Add(d).UTC().Format(time.RFC3339Nano)
	}
	for _, id := range recipients {
		if _, err := tx.ExecContext(ctx, "INSERT INTO mail_acks(message_id,task_id,recipient_id,due_at) VALUES(?,?,?,?)", message, r.TaskID, id, due); err != nil {
			return err
		}
	}
	return nil
}

func ackScope(ctx context.Context, tx *sql.Tx, p Principal, r MailAckRequest) (string, error) {
	task, _, err := resolveReply(ctx, tx, p, MailReplyRequest{MailSendRequest: MailSendRequest{Context: r.Context}, MessageID: r.MessageID})
	if err != nil {
		return "", err
	}
	var withdrawn string
	err = tx.QueryRowContext(ctx, "SELECT withdrawn_at FROM mail_acks WHERE message_id=? AND recipient_id=?", r.MessageID, p.SubjectID).Scan(&withdrawn)
	if errors.Is(err, sql.ErrNoRows) || err == nil && withdrawn != "" {
		return "", failure("forbidden", "no active acknowledgement obligation for this recipient")
	}
	return task.ID, err
}

func (s *Store) AckMail(ctx context.Context, token string, r MailAckRequest) (Receipt, error) {
	return s.Mutate(ctx, token, operation(r.OperationID, "mail.ack", r.SessionID, r), func(ctx context.Context, tx *sql.Tx, p Principal, _ Receipt) error {
		_, err := ackScope(ctx, tx, p, r)
		return err
	}, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if _, err := OpenSession(ctx, tx, p, r.Context); err != nil {
			return Receipt{}, err
		}
		if _, err := ackScope(ctx, tx, p, r); err != nil {
			return Receipt{}, err
		}
		result, err := tx.ExecContext(ctx, "UPDATE mail_acks SET acked_at=?,reminder_state=CASE WHEN reminder_state IN ('pending','submitting') THEN 'suppressed' ELSE reminder_state END WHERE message_id=? AND recipient_id=? AND acked_at=''", now(), r.MessageID, p.SubjectID)
		if err != nil {
			return Receipt{}, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return Receipt{}, err
		}
		if n > 0 {
			if err = bumpSessionRead(ctx, tx, r.SessionID); err != nil {
				return Receipt{}, err
			}
		}
		return Receipt{ResourceID: r.MessageID, Outcome: "acknowledged"}, nil
	})
}

// The recipient ID is the authenticated actor; no caller-supplied attribution is accepted.
func readMailAcks(ctx context.Context, tx *sql.Tx, task, message string) ([]MailAck, error) {
	query := `SELECT k.message_id,k.task_id,k.recipient_id,a.name,k.due_at,k.acked_at,k.withdrawn_at FROM mail_acks k JOIN agents a ON a.id=k.recipient_id WHERE k.task_id=?`
	args := []any{task}
	if message == "" {
		query += " AND k.acked_at='' AND k.withdrawn_at=''"
	} else {
		query += " AND k.message_id=?"
		args = append(args, message)
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY k.message_id,k.recipient_id LIMIT 501", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []MailAck{}
	for rows.Next() {
		var a MailAck
		if err = rows.Scan(&a.MessageID, &a.TaskID, &a.RecipientID, &a.RecipientName, &a.DueAt, &a.AckedAt, &a.WithdrawnAt); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
