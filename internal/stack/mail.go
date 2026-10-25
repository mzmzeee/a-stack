package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"unicode"
	"unicode/utf8"
)

const migration4 = `
CREATE TABLE mail_messages(
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), sequence INTEGER NOT NULL CHECK(sequence>0),
 sender_id TEXT NOT NULL REFERENCES agents(id), received_at TEXT NOT NULL, body TEXT NOT NULL,
 UNIQUE(task_id,sequence), UNIQUE(id,task_id)
) WITHOUT ROWID;
CREATE TABLE mail_mentions(
 message_id TEXT NOT NULL, task_id TEXT NOT NULL, recipient_id TEXT NOT NULL REFERENCES agents(id),
 notice_state TEXT NOT NULL CHECK(notice_state IN ('pending','submitting','submitted','uncertain','suppressed','self')),
 state_at TEXT NOT NULL,
 PRIMARY KEY(message_id,recipient_id), FOREIGN KEY(message_id,task_id) REFERENCES mail_messages(id,task_id)
) WITHOUT ROWID;
CREATE INDEX mail_mentions_pending ON mail_mentions(notice_state,recipient_id,message_id);
CREATE INDEX mail_mentions_membership ON mail_mentions(task_id,recipient_id,notice_state,message_id);
`

const MaxMailBodyBytes = 64 << 10
const MaxMailMentions = 500
const mailResponseBudget = 768 << 10

type NoticeState string

const (
	NoticePending    NoticeState = "pending"
	NoticeSubmitting NoticeState = "submitting"
	NoticeSubmitted  NoticeState = "submitted"
	NoticeUncertain  NoticeState = "uncertain"
	NoticeSuppressed NoticeState = "suppressed"
	NoticeSelf       NoticeState = "self"
)

type MailMessage struct {
	ID         string   `json:"id"`
	TaskID     string   `json:"task_id"`
	Sequence   int64    `json:"sequence"`
	SenderID   string   `json:"sender_id"`
	SenderName string   `json:"sender_name"`
	ReceivedAt string   `json:"received_at"`
	Body       string   `json:"body"`
	MentionIDs []string `json:"mention_ids"`
}
type MailSendRequest struct {
	Context
	OperationID string   `json:"operation_id"`
	TaskID      string   `json:"task_id"`
	Body        string   `json:"body"`
	Mentions    []string `json:"mentions,omitempty"`
}
type MailReadRequest struct {
	Context
	TaskID string `json:"task_id"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type MailPage struct {
	TaskID   string        `json:"task_id"`
	Messages []MailMessage `json:"messages"`
	Cursor   string        `json:"cursor"`
	More     bool          `json:"more"`
}
type MailCheckRequest struct {
	Context
	Cursors   map[string]string `json:"cursors,omitempty"`
	AfterTask string            `json:"after_task,omitempty"`
	TaskLimit int               `json:"task_limit,omitempty"`
	Limit     int               `json:"limit,omitempty"`
}

// MailCheckPage.More is true when a Task page has more or next_task is set:
// more mail is waiting than this response holds.
type MailCheckPage struct {
	Tasks    []MailPage `json:"tasks"`
	NextTask string     `json:"next_task,omitempty"`
	More     bool       `json:"more"`
}

func nameStart(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}
func nameTail(c byte) bool { return nameStart(c) || c == '_' || c == '-' }
func mentionBoundary(r rune) bool {
	return unicode.IsSpace(r) || unicode.Is(unicode.Ps, r) || unicode.Is(unicode.Pi, r) || r == '<'
}
func mentionNames(body string, explicit []string) ([]string, error) {
	if err := validText(body, MaxMailBodyBytes, true); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, name := range explicit {
		if !agentName.MatchString(name) {
			return nil, failure("invalid_input", "invalid explicit mention name")
		}
		names[name] = true
		if len(names) > MaxMailMentions {
			return nil, failure("invalid_input", "message mentions more than 500 distinct agents")
		}
	}
	var previous rune
	var previousQuoteOpens, doubleQuoted, singleQuoted, escaped bool
	for i, r := range body {
		boundary := i == 0 || mentionBoundary(previous) || ((previous == '"' || previous == '\'') && previousQuoteOpens)
		if r == '@' && i+1 < len(body) && nameStart(body[i+1]) && boundary {
			end := i + 1
			for end < len(body) && nameTail(body[end]) {
				end++
			}
			name := body[i+1 : end]
			if len(name) > 64 {
				return nil, failure("invalid_input", "mention name exceeds 64 bytes")
			}
			names[name] = true
			if len(names) > MaxMailMentions {
				return nil, failure("invalid_input", "message mentions more than 500 distinct agents")
			}
		}
		quoteOpens := false
		if !escaped && r == '"' {
			quoteOpens = !doubleQuoted
			doubleQuoted = !doubleQuoted
		}
		if !escaped && r == '\'' {
			next, _ := utf8.DecodeRuneInString(body[i+1:])
			wordBefore := unicode.IsLetter(previous) || unicode.IsDigit(previous)
			wordAfter := unicode.IsLetter(next) || unicode.IsDigit(next)
			// Apostrophes within words/possessives do not open a quoted span.
			if !(wordBefore && (wordAfter || !singleQuoted)) {
				quoteOpens = !singleQuoted
				singleQuoted = !singleQuoted
			}
		}
		previousQuoteOpens = quoteOpens
		previous = r
		if r == '\\' {
			escaped = !escaped
		} else {
			escaped = false
		}
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}
func resolveMentions(ctx context.Context, tx *sql.Tx, task string, names []string) ([]string, error) {
	ids := make([]string, 0, len(names))
	for _, name := range names {
		var id string
		err := tx.QueryRowContext(ctx, "SELECT a.id FROM task_assignees m JOIN agents a ON a.id=m.agent_id WHERE m.task_id=? AND a.name=?", task, name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, failure("invalid_input", "mention target must be a current Task assignee")
		}
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}
func (s *Store) SendMail(ctx context.Context, token string, r MailSendRequest) (Receipt, error) {
	names, prepareErr := mentionNames(r.Body, r.Mentions)
	return s.Mutate(ctx, token, operation(r.OperationID, "mail.send", r.SessionID, r), func(ctx context.Context, tx *sql.Tx, p Principal, receipt Receipt) error {
		var taskID string
		err := tx.QueryRowContext(ctx, "SELECT task_id FROM mail_messages WHERE id=?", receipt.ResourceID).Scan(&taskID)
		if errors.Is(err, sql.ErrNoRows) {
			return failure("not_found", "message not found")
		}
		if err != nil {
			return err
		}
		_, err = ReadAssignedTask(ctx, tx, p, r.Context, taskID)
		return err
	}, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if _, err := OpenSession(ctx, tx, p, r.Context); err != nil {
			return Receipt{}, err
		}
		if _, err := ReadAssignedTask(ctx, tx, p, r.Context, r.TaskID); err != nil {
			return Receipt{}, err
		}
		if prepareErr != nil {
			return Receipt{}, prepareErr
		}
		recipients, err := resolveMentions(ctx, tx, r.TaskID, names)
		if err != nil {
			return Receipt{}, err
		}
		var sequence int64
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM mail_messages WHERE task_id=?", r.TaskID).Scan(&sequence); err != nil {
			return Receipt{}, err
		}
		id, timestamp := opaqueID(), now()
		_, err = tx.ExecContext(ctx, "INSERT INTO mail_messages VALUES(?,?,?,?,?,?)", id, r.TaskID, sequence, p.SubjectID, timestamp, r.Body)
		if err != nil {
			return Receipt{}, err
		}
		for _, recipient := range recipients {
			state := NoticePending
			if recipient == p.SubjectID {
				state = NoticeSelf
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO mail_mentions VALUES(?,?,?,?,?)", id, r.TaskID, recipient, state, timestamp); err != nil {
				return Receipt{}, err
			}
		}
		// The Task item's mail_count changes, so the Session read revision moves on
		// and a polling view sees the new message.
		if err = bumpSessionRead(ctx, tx, r.SessionID); err != nil {
			return Receipt{}, err
		}
		// Revision is the append-only Task sequence; body_ref names the one stored
		// message, never a second body or a response snapshot.
		return Receipt{ResourceID: id, BodyRef: id, Revision: sequence, Outcome: "sent"}, nil
	})
}

// Membership removal permanently suppresses undelivered references. Rejoining
// restores retained mail access but never resets an old notice to pending.
func suppressTaskMentions(ctx context.Context, tx *sql.Tx, task, recipient string) error {
	_, err := tx.ExecContext(ctx, "UPDATE mail_mentions SET notice_state=?,state_at=? WHERE task_id=? AND recipient_id=? AND notice_state IN (?,?)", NoticeSuppressed, now(), task, recipient, NoticePending, NoticeSubmitting)
	return err
}
func readMailMessage(ctx context.Context, tx *sql.Tx, task string, sequence int64) (MailMessage, error) {
	message := MailMessage{MentionIDs: []string{}}
	err := tx.QueryRowContext(ctx, "SELECT m.id,m.task_id,m.sequence,m.sender_id,COALESCE(a.name,''),m.received_at,m.body FROM mail_messages m LEFT JOIN agents a ON a.id=m.sender_id WHERE m.task_id=? AND m.sequence=?", task, sequence).Scan(&message.ID, &message.TaskID, &message.Sequence, &message.SenderID, &message.SenderName, &message.ReceivedAt, &message.Body)
	if err != nil {
		return message, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT recipient_id FROM mail_mentions WHERE message_id=? ORDER BY recipient_id", message.ID)
	if err != nil {
		return message, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return message, err
		}
		message.MentionIDs = append(message.MentionIDs, id)
	}
	return message, rows.Err()
}
func mailLimit(limit int) (int, error) {
	if limit == 0 {
		return 100, nil
	}
	if limit < 1 || limit > 500 {
		return 0, failure("invalid_input", "mail limit must be 1–500")
	}
	return limit, nil
}
func jsonBytes(v any) int { b, _ := json.Marshal(v); return len(b) }
