package stack

import (
	"context"
	"database/sql"
	"errors"
)

type MailReplyRequest struct {
	MailSendRequest
	MessageID       string `json:"message_id"`
	NoMentionSender bool   `json:"no_mention_sender,omitempty"`
}

type MailReplyResult struct {
	Receipt
	TaskID    string `json:"task_id"`
	TaskTitle string `json:"task_title"`
	Audience  string `json:"audience"`
}

// resolveReply checks the parent in the caller's fixed Session, including on retry.
func resolveReply(ctx context.Context, tx *sql.Tx, p Principal, r MailReplyRequest) (Task, string, error) {
	var taskID, sender string
	err := tx.QueryRowContext(ctx, `SELECT m.task_id, COALESCE(m.sender_id,'') FROM mail_messages m JOIN tasks t ON t.id=m.task_id WHERE m.id=? AND t.session_id=?`, r.MessageID, r.SessionID).Scan(&taskID, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, "", failure("not_found", "message not found in this Session")
	}
	if err != nil {
		return Task{}, "", err
	}
	task, err := ReadAssignedTask(ctx, tx, p, r.Context, taskID)
	if err != nil {
		return Task{}, "", err
	}
	if r.TaskID != "" && r.TaskID != taskID {
		return Task{}, "", failure("invalid_input", "--task conflicts with the original message's Task")
	}
	return task, sender, nil
}

func (s *Store) ReplyMail(ctx context.Context, token string, r MailReplyRequest) (MailReplyResult, error) {
	result := MailReplyResult{Audience: "shared with all current assignees"}
	names, prepareErr := mentionNames(r.Body, r.Mentions)
	resolve := func(ctx context.Context, tx *sql.Tx, p Principal) (string, error) {
		task, sender, err := resolveReply(ctx, tx, p, r)
		if err == nil {
			result.TaskID, result.TaskTitle = task.ID, task.Title
		}
		return sender, err
	}
	receipt, err := s.Mutate(ctx, token, operation(r.OperationID, "mail.reply", r.SessionID, r), func(ctx context.Context, tx *sql.Tx, p Principal, _ Receipt) error {
		_, err := resolve(ctx, tx, p)
		return err
	}, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if _, err := OpenSession(ctx, tx, p, r.Context); err != nil {
			return Receipt{}, err
		}
		sender, err := resolve(ctx, tx, p)
		if err != nil {
			return Receipt{}, err
		}
		if prepareErr != nil {
			return Receipt{}, prepareErr
		}
		// Membership is checked in this write transaction, not in a client preflight.
		if !r.NoMentionSender && sender != "" {
			var name string
			err = tx.QueryRowContext(ctx, `SELECT a.name FROM task_assignees m JOIN agents a ON a.id=m.agent_id WHERE m.task_id=? AND a.id=?`, result.TaskID, sender).Scan(&name)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return Receipt{}, err
			}
			if err == nil {
				names, err = mentionNames(r.Body, append(append([]string{}, r.Mentions...), name))
				if err != nil {
					return Receipt{}, err
				}
			}
		}
		send := r.MailSendRequest
		send.TaskID = result.TaskID
		return appendMailTx(ctx, tx, p, send, names)
	})
	result.Receipt = receipt
	return result, err
}

type MailRecipientsRequest struct {
	Context
	TaskID string `json:"task_id"`
}

type MailRecipient struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
}

type MailRecipients struct {
	TaskID     string          `json:"task_id"`
	TaskTitle  string          `json:"task_title"`
	Audience   string          `json:"audience"`
	Recipients []MailRecipient `json:"recipients"`
}

func (s *Store) MailRecipients(ctx context.Context, token string, r MailRecipientsRequest) (MailRecipients, error) {
	result := MailRecipients{Recipients: []MailRecipient{}}
	tx, p, _, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	task, err := ReadAssignedTask(ctx, tx, p, r.Context, r.TaskID)
	if err != nil {
		return result, err
	}
	result.TaskID, result.TaskTitle, result.Audience = task.ID, task.Title, "shared with all current assignees"
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name FROM task_assignees m JOIN agents a ON a.id=m.agent_id WHERE m.task_id=? ORDER BY a.name`, task.ID)
	if err != nil {
		return result, publicError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var recipient MailRecipient
		if err = rows.Scan(&recipient.AgentID, &recipient.AgentName); err != nil {
			return result, publicError(err)
		}
		result.Recipients = append(result.Recipients, recipient)
	}
	if err = rows.Err(); err != nil {
		return result, publicError(err)
	}
	return result, nil
}
