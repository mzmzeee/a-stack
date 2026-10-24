package stack

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
)

type mailCursor struct {
	TaskID   string `json:"task_id"`
	Sequence int64  `json:"sequence"`
}
type mailCheckCursor struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	TaskID    string `json:"task_id"`
}

func encodeMailCursor(task string, sequence int64) string {
	b, _ := json.Marshal(mailCursor{task, sequence})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeMailCursor(task, cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	if len(cursor) > 512 {
		return 0, failure("invalid_input", "invalid mail cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, failure("invalid_input", "invalid mail cursor")
	}
	var position mailCursor
	if decodeJSON(data, &position) != nil || position.TaskID != task || position.Sequence < 0 {
		return 0, failure("invalid_input", "mail cursor belongs to another Task or has an invalid position")
	}
	return position.Sequence, nil
}
func checkMailPosition(ctx context.Context, tx *sql.Tx, task string, sequence int64) error {
	if sequence == 0 {
		return nil
	}
	var exists int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM mail_messages WHERE task_id=? AND sequence=?", task, sequence).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return failure("invalid_input", "mail cursor position does not exist in this retained Task history")
	}
	return err
}

// readMailPageTx returns fit=false when the remaining aggregate budget cannot
// hold the first pending message. Its caller must not advance that Task cursor.
func readMailPageTx(ctx context.Context, tx *sql.Tx, task, cursor string, limit, budget int) (MailPage, bool, error) {
	page := MailPage{TaskID: task, Messages: []MailMessage{}}
	position, err := decodeMailCursor(task, cursor)
	if err != nil {
		return page, false, err
	}
	if err = checkMailPosition(ctx, tx, task, position); err != nil {
		return page, false, err
	}
	page.Cursor = encodeMailCursor(task, position)
	used := jsonBytes(page)
	if used > budget {
		return page, false, nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT sequence FROM mail_messages WHERE task_id=? AND sequence>? ORDER BY sequence LIMIT ?", task, position, limit+1)
	if err != nil {
		return page, false, err
	}
	sequences := []int64{}
	for rows.Next() {
		var seq int64
		if err = rows.Scan(&seq); err != nil {
			rows.Close()
			return page, false, err
		}
		sequences = append(sequences, seq)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, false, err
	}
	for _, sequence := range sequences {
		if len(page.Messages) == limit {
			page.More = true
			break
		}
		message, err := readMailMessage(ctx, tx, task, sequence)
		if err != nil {
			return page, false, err
		}
		next := encodeMailCursor(task, sequence)
		extra := jsonBytes(message) + len(next) - len(page.Cursor)
		if len(page.Messages) != 0 {
			extra++
		}
		if used+extra > budget {
			page.More = true
			if len(page.Messages) == 0 {
				return page, false, nil
			}
			break
		}
		page.Messages = append(page.Messages, message)
		page.Cursor = next
		used += extra
	}
	return page, true, nil
}
func (s *Store) ReadMail(ctx context.Context, token string, r MailReadRequest) (MailPage, error) {
	empty := MailPage{}
	limit, err := mailLimit(r.Limit)
	if err != nil {
		return empty, err
	}
	tx, p, _, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	// A browser view credential reads the mail of any Task in its own Session
	// (ADR 0003 amendment); everyone else must be a current assignee. The read
	// writes nothing: no read position, receipt or notice.
	if p.Kind == BrowserCapability {
		_, err = taskInSession(ctx, tx, r.TaskID, r.SessionID)
	} else {
		_, err = ReadAssignedTask(ctx, tx, p, r.Context, r.TaskID)
	}
	if err != nil {
		return empty, err
	}
	page, fit, err := readMailPageTx(ctx, tx, r.TaskID, r.Cursor, limit, mailResponseBudget)
	if err != nil {
		return empty, publicError(err)
	}
	if !fit {
		return empty, failure("storage_unavailable", "stored mail message exceeds the response budget")
	}
	return page, nil
}
func encodeMailCheckCursor(session, agent, task string) string {
	b, _ := json.Marshal(mailCheckCursor{session, agent, task})
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeMailCheckCursor(session, agent, cursor string) (string, error) {
	if cursor == "" {
		return "", nil
	}
	if len(cursor) > 512 {
		return "", failure("invalid_input", "invalid mail-check continuation")
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", failure("invalid_input", "invalid mail-check continuation")
	}
	var c mailCheckCursor
	if decodeJSON(data, &c) != nil || c.SessionID != session || c.AgentID != agent || !validToken(c.TaskID) {
		return "", failure("invalid_input", "mail-check continuation belongs to another Session/caller")
	}
	return c.TaskID, nil
}
func (s *Store) CheckMail(ctx context.Context, token string, r MailCheckRequest) (MailCheckPage, error) {
	empty := MailCheckPage{}
	limit, err := mailLimit(r.Limit)
	if err != nil {
		return empty, err
	}
	if r.TaskLimit == 0 {
		r.TaskLimit = 50
	}
	if r.TaskLimit < 1 || r.TaskLimit > 100 {
		return empty, failure("invalid_input", "Task scan limit must be 1–100")
	}
	tx, p, _, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if p.Kind != AgentCapability {
		return empty, failure("forbidden", "mail check requires an Agent credential and current Task assignments")
	}
	after, err := decodeMailCheckCursor(r.SessionID, p.SubjectID, r.AfterTask)
	if err != nil {
		return empty, err
	}
	// Only the cursors of the Tasks this page scans are decoded, by
	// readMailPageTx below. A saved map can hold hundreds of other Tasks.
	for task := range r.Cursors {
		if !validToken(task) {
			return empty, failure("invalid_input", "invalid Task key in caller cursors")
		}
	}
	// This query is the assignment check: inside this read transaction, after
	// ReadSession proved current Session membership, it returns only Tasks of
	// this Session that the caller is assigned to now. A removal commits before
	// or after this transaction, so a removed Agent never sees that Task again.
	ids, err := readIDs(ctx, tx, "SELECT task_id FROM task_assignees WHERE session_id=? AND agent_id=? AND task_id>? ORDER BY task_id LIMIT ?", r.SessionID, p.SubjectID, after, r.TaskLimit+1)
	if err != nil {
		return empty, err
	}
	result := MailCheckPage{Tasks: []MailPage{}}
	used := jsonBytes(result)
	messages := 0
	last := ""
	for _, task := range ids {
		if len(result.Tasks) == r.TaskLimit || messages == limit {
			result.NextTask = encodeMailCheckCursor(r.SessionID, p.SubjectID, last)
			break
		}
		// Reserve the bounded scan-continuation envelope in addition to each exact
		// encoded per-Task page; no unread position is written or inferred.
		page, fit, err := readMailPageTx(ctx, tx, task, r.Cursors[task], limit-messages, mailResponseBudget-used-1024)
		if err != nil {
			return empty, publicError(err)
		}
		if !fit {
			if len(result.Tasks) == 0 {
				return empty, failure("storage_unavailable", "stored mail message exceeds the response budget")
			}
			result.NextTask = encodeMailCheckCursor(r.SessionID, p.SubjectID, last)
			break
		}
		used += jsonBytes(page) + 1
		result.Tasks = append(result.Tasks, page)
		messages += len(page.Messages)
		last = task
	}
	return result, nil
}
