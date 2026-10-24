package stack

import (
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
