package stack

import (
	"context"
	"database/sql"
	"sync/atomic"
	"time"
)

// migration6 stores the archive command and the time a Task ended, and lets the
// Operator appear in Task history. archived_at is set only by `task archive`;
// ended_at is when the Task last became Finished or Cancelled, taken from the
// history for older Tasks. task_history is rebuilt because SQLite cannot relax
// NOT NULL in place: an Operator entry has actor_kind 'operator' and no actor_id,
// while Agent entries keep their foreign key to agents.
const migration6 = `
ALTER TABLE tasks ADD COLUMN archived_at TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN ended_at TEXT NOT NULL DEFAULT '';
UPDATE tasks SET ended_at=COALESCE((SELECT h.received_at FROM task_history h WHERE h.task_id=tasks.id AND h.action='task.status' AND json_extract(h.changes,'$.progress') IN ('finished','cancelled') ORDER BY h.sequence DESC LIMIT 1),updated_at) WHERE progress IN ('finished','cancelled');
CREATE TABLE task_history_v6(sequence INTEGER PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),actor_kind TEXT NOT NULL DEFAULT 'agent',actor_id TEXT REFERENCES agents(id),action TEXT NOT NULL,revision INTEGER NOT NULL,received_at TEXT NOT NULL,changes TEXT NOT NULL,
 CHECK((actor_kind='agent' AND actor_id IS NOT NULL) OR (actor_kind='operator' AND actor_id IS NULL)));
INSERT INTO task_history_v6(sequence,task_id,actor_kind,actor_id,action,revision,received_at,changes) SELECT sequence,task_id,'agent',actor_id,action,revision,received_at,changes FROM task_history;
DROP TABLE task_history;
ALTER TABLE task_history_v6 RENAME TO task_history;
CREATE INDEX task_history_task ON task_history(task_id,sequence);
`

// ArchiveAge is how long a Finished or Cancelled Task stays visible before the
// age rule archives it.
const ArchiveAge = 24 * time.Hour

// fixedClock lets tests pin the service time; nil means the real clock.
var fixedClock atomic.Pointer[time.Time]

func clockNow() time.Time {
	if t := fixedClock.Load(); t != nil {
		return *t
	}
	return time.Now()
}

// archiveMark counts the done Tasks of a Session that are ArchiveAge old or
// older, without an archive command, plus its quiet Tasks (see quietMark). It
// grows when the age rule archives a Task or a Task turns quiet, which changes
// no revision. Neither count falls without a change, so the sum grows too. The
// snapshot ETag includes it, so that an open view of an idle Session loads the
// Tasks again then. The count is kept until the Session's read revision
// changes or the next Task gets old enough or turns quiet.
func (s *Store) archiveMark(ctx context.Context, tx *sql.Tx, session Session) (int64, error) {
	at := clockNow()
	s.marksMu.Lock()
	m, ok := s.marks[session.ID]
	s.marksMu.Unlock()
	if ok && m.revision == session.ReadRevision && at.Before(m.next) {
		return m.count, nil
	}
	cutoff := at.Add(-ArchiveAge).UTC().Format(time.RFC3339Nano)
	var next string
	m = archiveMarkState{revision: session.ReadRevision, next: at.Add(ArchiveAge)}
	err := tx.QueryRowContext(ctx, "SELECT count(*) FILTER (WHERE ended_at<=?1),COALESCE(min(ended_at) FILTER (WHERE ended_at>?1),'') FROM tasks WHERE session_id=?2 AND progress IN ('finished','cancelled') AND archived_at=''", cutoff, session.ID).Scan(&m.count, &next)
	if err != nil {
		return 0, storageError(err)
	}
	// With no younger done Task, only a change can move the mark, and a change
	// raises the read revision.
	if ended, err := time.Parse(time.RFC3339Nano, next); err == nil {
		m.next = ended.Add(ArchiveAge)
	}
	quiet, quietNext, err := quietMark(ctx, tx, session.ID, at)
	if err != nil {
		return 0, err
	}
	var overdue int64
	var ackNext string
	err = tx.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE julianday(k.due_at)<=julianday(?1)),COALESCE(min(k.due_at) FILTER (WHERE julianday(k.due_at)>julianday(?1)),'') FROM mail_acks k JOIN tasks t ON t.id=k.task_id WHERE t.session_id=?2 AND k.acked_at='' AND k.withdrawn_at='' AND k.due_at<>''`, at.UTC().Format(time.RFC3339Nano), session.ID).Scan(&overdue, &ackNext)
	if err != nil {
		return 0, storageError(err)
	}
	m.count += overdue
	if due, err := time.Parse(time.RFC3339Nano, ackNext); err == nil && due.Before(m.next) {
		m.next = due
	}
	m.count += quiet
	if !quietNext.IsZero() && quietNext.Before(m.next) {
		m.next = quietNext
	}
	s.marksMu.Lock()
	if s.marks == nil {
		s.marks = map[string]archiveMarkState{}
	}
	s.marks[session.ID] = m
	s.marksMu.Unlock()
	return m.count, nil
}

type archiveMarkState struct {
	revision, count int64
	next            time.Time
}

// deriveArchive sets Archived and ArchivedReason. Only a Finished or Cancelled
// Task can be archived. The command wins; then the chain rule (the Task has
// dependents and all of them are Finished or Cancelled, so the end of a chain
// stays visible); then the age rule (ended ArchiveAge ago or more).
func deriveArchive(t *Task, at time.Time) {
	t.Archived, t.ArchivedReason = false, ""
	if t.Progress != "finished" && t.Progress != "cancelled" {
		return
	}
	switch {
	case t.ArchivedAt != "":
		t.ArchivedReason = "command"
	case t.chainDone:
		t.ArchivedReason = "chain"
	default:
		ended, err := time.Parse(time.RFC3339Nano, t.EndedAt)
		if err != nil || at.Sub(ended) < ArchiveAge {
			return
		}
		t.ArchivedReason = "age"
	}
	t.Archived = true
}
