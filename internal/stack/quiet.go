package stack

import (
	"context"
	"database/sql"
	"time"
)

// QuietAfter is how long a Working Task may go without a change or Task mail
// before it counts as quiet: its Agent may have stopped (usage limit, crash,
// closed tab). It is a variable only so that tests can shorten it.
var QuietAfter = 30 * time.Minute

// QuietAt is when a Working Task turns quiet: QuietAfter after its last change
// or Task mail, whichever came later. It is zero for a Task that is not
// Working, and for one that waits for the Operator's answer. Nothing is
// stored: the reads derive it from updated_at and the last mail.
func (t Task) QuietAt() time.Time {
	if t.Progress != "working" || t.NeedsYou {
		return time.Time{}
	}
	last, _ := time.Parse(time.RFC3339Nano, t.UpdatedAt)
	if mail, err := time.Parse(time.RFC3339Nano, t.LastMailAt); err == nil && mail.After(last) {
		last = mail
	}
	return last.Add(QuietAfter)
}

func (t Task) quiet(at time.Time) bool {
	q := t.QuietAt()
	return !q.IsZero() && !at.Before(q)
}

// quietMark counts the quiet Tasks of a Session and returns when the next one
// turns quiet (zero when none will without a change). Like the age rule,
// turning quiet changes no revision, so the snapshot ETag carries this count.
func quietMark(ctx context.Context, tx *sql.Tx, session string, at time.Time) (int64, time.Time, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.updated_at,COALESCE((SELECT m.received_at FROM mail_messages m WHERE m.task_id=t.id ORDER BY m.sequence DESC LIMIT 1),'')
 FROM tasks t WHERE t.session_id=? AND t.progress='working' AND NOT EXISTS(SELECT 1 FROM task_questions q WHERE q.task_id=t.id AND q.resolved_at IS NULL)`, session)
	if err != nil {
		return 0, time.Time{}, storageError(err)
	}
	defer rows.Close()
	var count int64
	var next time.Time
	for rows.Next() {
		t := Task{Progress: "working"}
		if err = rows.Scan(&t.UpdatedAt, &t.LastMailAt); err != nil {
			return 0, time.Time{}, storageError(err)
		}
		if q := t.QuietAt(); !at.Before(q) {
			count++
		} else if next.IsZero() || q.Before(next) {
			next = q
		}
	}
	if err = rows.Err(); err != nil {
		return 0, time.Time{}, storageError(err)
	}
	return count, next, nil
}
