package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

const migration3 = `
ALTER TABLE sessions ADD COLUMN graph_revision INTEGER NOT NULL DEFAULT 1;
CREATE TABLE tasks(
 id TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id), title TEXT NOT NULL, ticket_ref TEXT NOT NULL DEFAULT '',
 description_ref TEXT REFERENCES task_content(id), report_ref TEXT REFERENCES task_content(id), reason_ref TEXT REFERENCES task_content(id), result_ref TEXT REFERENCES task_content(id),
 progress TEXT NOT NULL CHECK(progress IN ('waiting','working','finished','cancelled')), revision INTEGER NOT NULL DEFAULT 1,
 created_by TEXT NOT NULL REFERENCES agents(id), updated_by TEXT NOT NULL REFERENCES agents(id),
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, report_at TEXT NOT NULL DEFAULT '', UNIQUE(id,session_id)
) WITHOUT ROWID;
CREATE INDEX tasks_session ON tasks(session_id,id);
CREATE UNIQUE INDEX tasks_ticket ON tasks(session_id,ticket_ref) WHERE ticket_ref!='';
CREATE TABLE task_content(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),kind TEXT NOT NULL,body TEXT NOT NULL,actor_id TEXT NOT NULL REFERENCES agents(id),received_at TEXT NOT NULL) WITHOUT ROWID;
CREATE INDEX task_content_task ON task_content(task_id,id);
CREATE TABLE task_assignees(task_id TEXT NOT NULL,agent_id TEXT NOT NULL,added_by TEXT NOT NULL REFERENCES agents(id),added_at TEXT NOT NULL,session_id TEXT NOT NULL,PRIMARY KEY(task_id,agent_id),FOREIGN KEY(task_id,session_id) REFERENCES tasks(id,session_id),FOREIGN KEY(agent_id,session_id) REFERENCES agents(id,session_id)) WITHOUT ROWID;
CREATE INDEX task_assignees_session ON task_assignees(session_id,task_id,agent_id);
CREATE INDEX task_assignees_agent ON task_assignees(agent_id,task_id);
CREATE TABLE task_questions(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),question TEXT NOT NULL,asker_id TEXT NOT NULL REFERENCES agents(id),asked_at TEXT NOT NULL,note TEXT NOT NULL DEFAULT '',resolver_id TEXT REFERENCES agents(id),resolved_at TEXT) WITHOUT ROWID;
CREATE UNIQUE INDEX task_one_question ON task_questions(task_id) WHERE resolved_at IS NULL;
CREATE INDEX task_questions_task ON task_questions(task_id,id);
CREATE TABLE task_references(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),location TEXT NOT NULL,revision TEXT NOT NULL,added_by TEXT NOT NULL REFERENCES agents(id),added_at TEXT NOT NULL,removed_by TEXT REFERENCES agents(id),removed_at TEXT) WITHOUT ROWID;
CREATE UNIQUE INDEX task_active_reference ON task_references(task_id,location,revision) WHERE removed_at IS NULL;
CREATE INDEX task_references_task ON task_references(task_id,id);
CREATE TABLE task_history(sequence INTEGER PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),actor_id TEXT NOT NULL REFERENCES agents(id),action TEXT NOT NULL,revision INTEGER NOT NULL,received_at TEXT NOT NULL,changes TEXT NOT NULL);
CREATE INDEX task_history_task ON task_history(task_id,sequence);
CREATE TABLE dependencies(id TEXT PRIMARY KEY,session_id TEXT NOT NULL REFERENCES sessions(id),prerequisite_id TEXT NOT NULL,dependent_id TEXT NOT NULL,created_by TEXT NOT NULL REFERENCES agents(id),created_at TEXT NOT NULL,UNIQUE(session_id,prerequisite_id,dependent_id),CHECK(prerequisite_id!=dependent_id),FOREIGN KEY(prerequisite_id,session_id) REFERENCES tasks(id,session_id),FOREIGN KEY(dependent_id,session_id) REFERENCES tasks(id,session_id)) WITHOUT ROWID;
CREATE INDEX dependencies_session ON dependencies(session_id,id);
CREATE INDEX dependencies_from ON dependencies(session_id,prerequisite_id,dependent_id);
CREATE INDEX dependencies_to ON dependencies(session_id,dependent_id,prerequisite_id);
CREATE TABLE graph_history(sequence INTEGER PRIMARY KEY,session_id TEXT NOT NULL REFERENCES sessions(id),actor_id TEXT NOT NULL REFERENCES agents(id),action TEXT NOT NULL,edge_id TEXT NOT NULL,prerequisite_id TEXT NOT NULL REFERENCES tasks(id),dependent_id TEXT NOT NULL REFERENCES tasks(id),revision INTEGER NOT NULL,received_at TEXT NOT NULL);
CREATE INDEX graph_history_session ON graph_history(session_id,sequence);
`

const MaxTaskTextBytes = 64 << 10
const MaxTaskNoteBytes = 2048

// ReportPreview and QuestionPreview are limited to 160 Unicode code points in the read projection.
// LastAction is the action of the Task's latest history entry, such as task.status; it happened at UpdatedAt.
type Task struct {
	ReportPreview                                   string `json:"report_preview,omitempty"`
	QuestionPreview                                 string `json:"question_preview,omitempty"`
	LastAction                                      string `json:"last_action,omitempty"`
	ID                                              string `json:"id"`
	SessionID                                       string `json:"session_id"`
	Title                                           string `json:"title"`
	TicketRef                                       string `json:"ticket_ref,omitempty"`
	Progress                                        string `json:"progress"`
	Revision                                        int64  `json:"revision"`
	AssigneeCount                                   int    `json:"assignee_count"`
	Unassigned                                      bool   `json:"unassigned"`
	NeedsYou                                        bool   `json:"needs_you"`
	ReferenceCount                                  int    `json:"reference_count"`
	PrerequisiteCount                               int    `json:"prerequisite_count"`
	CancelledPrerequisiteCount                      int    `json:"cancelled_prerequisite_count"`
	CreatedBy                                       string `json:"created_by"`
	UpdatedBy                                       string `json:"updated_by"`
	CreatedAt                                       string `json:"created_at"`
	UpdatedAt                                       string `json:"updated_at"`
	ReportAt                                        string `json:"report_at,omitempty"`
	descriptionRef, reportRef, reasonRef, resultRef string
}
type Question struct {
	ID         string `json:"id"`
	TaskID     string `json:"task_id"`
	Question   string `json:"question"`
	AskerID    string `json:"asker_id"`
	AskedAt    string `json:"asked_at"`
	Note       string `json:"note,omitempty"`
	ResolverID string `json:"resolver_id,omitempty"`
	ResolvedAt string `json:"resolved_at,omitempty"`
}
type TaskDetail struct {
	Task
	ReadRevision int64     `json:"read_revision"`
	Description  string    `json:"description"`
	Report       string    `json:"report"`
	Reason       string    `json:"reason"`
	Result       string    `json:"result"`
	Question     *Question `json:"question,omitempty"`
}
type TaskCreateRequest struct {
	Context
	OperationID string `json:"operation_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	TicketRef   string `json:"ticket_ref,omitempty"`
}
type TaskChangeRequest struct {
	Context
	OperationID       string  `json:"operation_id"`
	TaskID            string  `json:"task_id"`
	ExpectedRevision  int64   `json:"expected_revision"`
	Title             *string `json:"title,omitempty"`
	Description       *string `json:"description,omitempty"`
	TicketRef         *string `json:"ticket_ref,omitempty"`
	Report            *string `json:"report,omitempty"`
	AgentID           string  `json:"agent_id,omitempty"`
	Progress          string  `json:"progress,omitempty"`
	Reason            string  `json:"reason,omitempty"`
	Summary           string  `json:"summary,omitempty"`
	Question          string  `json:"question,omitempty"`
	Note              string  `json:"note,omitempty"`
	Location          string  `json:"location,omitempty"`
	ReferenceRevision string  `json:"reference_revision,omitempty"`
	ReferenceID       string  `json:"reference_id,omitempty"`
}
type TaskChange struct {
	Title       *string  `json:"title,omitempty"`
	TicketRef   *string  `json:"ticket_ref,omitempty"`
	TextIDs     []string `json:"text_ids,omitempty"`
	AgentID     string   `json:"agent_id,omitempty"`
	Progress    string   `json:"progress,omitempty"`
	QuestionID  string   `json:"question_id,omitempty"`
	ReferenceID string   `json:"reference_id,omitempty"`
}
type TaskContent struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Body       string `json:"body"`
	ActorID    string `json:"actor_id"`
	ReceivedAt string `json:"received_at"`
}
type FileReference struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	Location  string `json:"location"`
	Revision  string `json:"revision,omitempty"`
	AddedBy   string `json:"added_by"`
	AddedAt   string `json:"added_at"`
	RemovedBy string `json:"removed_by,omitempty"`
	RemovedAt string `json:"removed_at,omitempty"`
}
type Dependency struct {
	ID                   string `json:"id"`
	PrerequisiteID       string `json:"prerequisite_id"`
	DependentID          string `json:"dependent_id"`
	PrerequisiteProgress string `json:"prerequisite_progress"`
	DependentProgress    string `json:"dependent_progress"`
	CreatedBy            string `json:"created_by"`
	CreatedAt            string `json:"created_at"`
}
type DependencyRequest struct {
	Context
	OperationID           string `json:"operation_id"`
	ExpectedGraphRevision int64  `json:"expected_graph_revision"`
	PrerequisiteID        string `json:"prerequisite_id"`
	DependentID           string `json:"dependent_id"`
}

const taskColumns = `t.id,t.session_id,t.title,t.ticket_ref,t.progress,t.revision,t.created_by,t.updated_by,t.created_at,t.updated_at,t.report_at,COALESCE(t.description_ref,''),COALESCE(t.report_ref,''),COALESCE(t.reason_ref,''),COALESCE(t.result_ref,''),
 (SELECT count(*) FROM task_assignees a WHERE a.task_id=t.id),EXISTS(SELECT 1 FROM task_questions q WHERE q.task_id=t.id AND q.resolved_at IS NULL),
 (SELECT count(*) FROM task_references r WHERE r.task_id=t.id AND r.removed_at IS NULL),
 (SELECT count(*) FROM dependencies d WHERE d.session_id=t.session_id AND d.dependent_id=t.id),
 (SELECT count(*) FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id WHERE d.session_id=t.session_id AND d.dependent_id=t.id AND p.progress='cancelled'),
 COALESCE((SELECT substr(c.body,1,160) FROM task_content c WHERE c.id=t.report_ref),''),
 COALESCE((SELECT substr(q.question,1,160) FROM task_questions q WHERE q.task_id=t.id AND q.resolved_at IS NULL),''),
 COALESCE((SELECT h.action FROM task_history h WHERE h.task_id=t.id ORDER BY h.sequence DESC LIMIT 1),'')`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.SessionID, &t.Title, &t.TicketRef, &t.Progress, &t.Revision, &t.CreatedBy, &t.UpdatedBy, &t.CreatedAt, &t.UpdatedAt, &t.ReportAt, &t.descriptionRef, &t.reportRef, &t.reasonRef, &t.resultRef, &t.AssigneeCount, &t.NeedsYou, &t.ReferenceCount, &t.PrerequisiteCount, &t.CancelledPrerequisiteCount, &t.ReportPreview, &t.QuestionPreview, &t.LastAction)
	t.Unassigned = t.AssigneeCount == 0
	if errors.Is(err, sql.ErrNoRows) {
		return t, failure("not_found", "Task not found in Session")
	}
	if err != nil {
		return t, storageError()
	}
	return t, nil
}
func taskInSession(ctx context.Context, tx *sql.Tx, id, session string) (Task, error) {
	if !validToken(id) {
		return Task{}, failure("invalid_input", "Task ID required")
	}
	return scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM tasks t WHERE t.id=? AND t.session_id=?", id, session))
}

// ReadTask and ReadAssignedTask are the small shared authority boundary used by
// later mail reads/writes. OpenSession must also be called for a domain write.
func ReadTask(ctx context.Context, tx *sql.Tx, p Principal, ref Context, id string) (Task, error) {
	if _, err := ReadSession(ctx, tx, p, ref); err != nil {
		return Task{}, err
	}
	return taskInSession(ctx, tx, id, ref.SessionID)
}
func RequireTaskAssignee(ctx context.Context, tx *sql.Tx, p Principal, t Task) error {
	if p.Kind != AgentCapability {
		return failure("forbidden", "current Task assignee required")
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM task_assignees WHERE task_id=? AND agent_id=?", t.ID, p.SubjectID).Scan(&count); err != nil {
		return storageError()
	}
	if count != 1 {
		return failure("forbidden", "current Task assignee required")
	}
	return nil
}
func ReadAssignedTask(ctx context.Context, tx *sql.Tx, p Principal, ref Context, id string) (Task, error) {
	t, err := ReadTask(ctx, tx, p, ref, id)
	if err == nil {
		err = RequireTaskAssignee(ctx, tx, p, t)
	}
	return t, err
}
func replayTask(assigned bool) func(context.Context, *sql.Tx, Principal, Receipt) error {
	return func(ctx context.Context, tx *sql.Tx, p Principal, r Receipt) error {
		var sessionID string
		if err := tx.QueryRowContext(ctx, "SELECT session_id FROM tasks WHERE id=?", r.ResourceID).Scan(&sessionID); errors.Is(err, sql.ErrNoRows) {
			return failure("not_found", "Task not found")
		} else if err != nil {
			return storageError()
		}
		session, err := loadSession(ctx, tx, sessionID)
		if err != nil {
			return err
		}
		task, err := ReadTask(ctx, tx, p, Context{session.WorkspaceID, session.ID}, r.ResourceID)
		if err == nil && assigned {
			err = RequireTaskAssignee(ctx, tx, p, task)
		}
		return err
	}
}
func validText(body string, max int, required bool) error {
	if len(body) > max || !utf8.ValidString(body) || (required && strings.TrimSpace(body) == "") {
		return failure("invalid_input", "text is empty, invalid UTF-8 or exceeds its byte limit")
	}
	return nil
}
func insertContent(ctx context.Context, tx *sql.Tx, p Principal, taskID, kind, body string) (string, error) {
	id := opaqueID()
	_, err := tx.ExecContext(ctx, "INSERT INTO task_content VALUES(?,?,?,?,?,?)", id, taskID, kind, body, p.SubjectID, now())
	return id, err
}
func contentBody(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	var body string
	err := tx.QueryRowContext(ctx, "SELECT body FROM task_content WHERE id=?", id).Scan(&body)
	return body, err
}
func recordTask(ctx context.Context, tx *sql.Tx, p Principal, taskID, action string, revision int64, change TaskChange) error {
	b, _ := json.Marshal(change)
	_, err := tx.ExecContext(ctx, "INSERT INTO task_history(task_id,actor_id,action,revision,received_at,changes) VALUES(?,?,?,?,?,?)", taskID, p.SubjectID, action, revision, now(), string(b))
	return err
}
func bumpGraph(ctx context.Context, tx *sql.Tx, session string) error {
	_, err := tx.ExecContext(ctx, "UPDATE sessions SET graph_revision=graph_revision+1,read_revision=read_revision+1 WHERE id=?", session)
	return err
}
func checkTicket(ctx context.Context, tx *sql.Tx, session, task, ticket string) error {
	if err := validText(ticket, 1024, false); err != nil {
		return err
	}
	if ticket == "" {
		return nil
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE session_id=? AND ticket_ref=? AND id!=?", session, ticket, task).Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return failure("conflict", "ticket reference already belongs to a Task in this Session")
	}
	return nil
}
func (s *Store) CreateTask(ctx context.Context, token string, r TaskCreateRequest) (Receipt, error) {
	return s.Mutate(ctx, token, operation(r.OperationID, "task.create", r.SessionID, r), replayTask(false), func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		session, err := OpenSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		if err = RequireOrchestrator(p, session); err != nil {
			return Receipt{}, err
		}
		if err = validText(r.Title, 256, true); err != nil {
			return Receipt{}, err
		}
		if err = validText(r.Description, MaxTaskTextBytes, false); err != nil {
			return Receipt{}, err
		}
		if err = checkTicket(ctx, tx, r.SessionID, "", r.TicketRef); err != nil {
			return Receipt{}, err
		}
		id, timestamp := opaqueID(), now()
		_, err = tx.ExecContext(ctx, "INSERT INTO tasks(id,session_id,title,ticket_ref,progress,created_by,updated_by,created_at,updated_at) VALUES(?,?,?,?,'waiting',?,?,?,?)", id, r.SessionID, r.Title, r.TicketRef, p.SubjectID, p.SubjectID, timestamp, timestamp)
		if err != nil {
			return Receipt{}, err
		}
		description, err := insertContent(ctx, tx, p, id, "description", r.Description)
		if err != nil {
			return Receipt{}, err
		}
		reason, err := insertContent(ctx, tx, p, id, "reason", "Not started")
		if err != nil {
			return Receipt{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE tasks SET description_ref=?,reason_ref=? WHERE id=?", description, reason, id); err != nil {
			return Receipt{}, err
		}
		if err = recordTask(ctx, tx, p, id, "task.create", 1, TaskChange{Title: &r.Title, TicketRef: &r.TicketRef, TextIDs: []string{description, reason}, Progress: "waiting"}); err != nil {
			return Receipt{}, err
		}
		if err = bumpGraph(ctx, tx, r.SessionID); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: id, Revision: 1, Outcome: "created"}, nil
	})
}
func taskRevision(expected int64, t Task) error {
	err := expectedRevision(expected, t.Revision)
	if e, ok := err.(*Error); ok && e.Code == "conflict" {
		e.CurrentProgress = t.Progress
	}
	return err
}
func taskChangeFields(action string, r TaskChangeRequest) error {
	allowed := ""
	switch action {
	case "edit":
		allowed = "title description ticket_ref"
	case "assign", "unassign":
		allowed = "agent_id"
	case "status":
		allowed = "progress reason summary"
	case "reopen":
		allowed = "reason"
	case "report":
		allowed = "report"
	case "attention.set":
		allowed = "question"
	case "attention.resolve":
		allowed = "note"
	case "reference.add":
		allowed = "location reference_revision"
	case "reference.remove":
		allowed = "reference_id"
	default:
		return failure("invalid_input", "unknown Task action")
	}
	for key, present := range map[string]bool{"title": r.Title != nil, "description": r.Description != nil, "ticket_ref": r.TicketRef != nil, "agent_id": r.AgentID != "", "progress": r.Progress != "", "reason": r.Reason != "", "summary": r.Summary != "", "report": r.Report != nil, "question": r.Question != "", "note": r.Note != "", "location": r.Location != "", "reference_revision": r.ReferenceRevision != "", "reference_id": r.ReferenceID != ""} {
		if present && !strings.Contains(" "+allowed+" ", " "+key+" ") {
			return failure("invalid_input", "field does not apply to this Task action")
		}
	}
	return nil
}
func (s *Store) ChangeTask(ctx context.Context, token, action string, r TaskChangeRequest) (Receipt, error) {
	if err := taskChangeFields(action, r); err != nil {
		return Receipt{}, err
	}
	assigned := action != "edit" && action != "assign" && action != "unassign"
	return s.Mutate(ctx, token, operation(r.OperationID, "task."+action, r.SessionID, r), replayTask(assigned), func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		session, err := OpenSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		task, err := taskInSession(ctx, tx, r.TaskID, r.SessionID)
		if err != nil {
			return Receipt{}, err
		}
		if assigned {
			err = RequireTaskAssignee(ctx, tx, p, task)
		} else {
			err = RequireOrchestrator(p, session)
		}
		if err != nil {
			return Receipt{}, err
		}
		if action == "attention.set" {
			question, err := openQuestion(ctx, tx, task.ID)
			if err != nil {
				return Receipt{}, err
			}
			if question != nil {
				return Receipt{}, &Error{Code: "conflict", Message: "Task already has an open Operator question", CurrentRevision: &task.Revision, CurrentProgress: task.Progress, CurrentQuestion: question}
			}
		}
		if err = taskRevision(r.ExpectedRevision, task); err != nil {
			return Receipt{}, err
		}
		change := TaskChange{}
		outcome := action
		setText := func(kind, body string, current *string, max int, required bool) error {
			if err := validText(body, max, required); err != nil {
				return err
			}
			old, err := contentBody(ctx, tx, *current)
			if err != nil {
				return err
			}
			if old == body && *current != "" {
				return nil
			}
			id, err := insertContent(ctx, tx, p, task.ID, kind, body)
			if err != nil {
				return err
			}
			*current = id
			change.TextIDs = append(change.TextIDs, id)
			return nil
		}
		switch action {
		case "edit":
			if r.Title == nil && r.Description == nil && r.TicketRef == nil {
				return Receipt{}, failure("invalid_input", "at least one definition field required")
			}
			if r.Title != nil {
				if err = validText(*r.Title, 256, true); err != nil {
					return Receipt{}, err
				}
				task.Title = *r.Title
				change.Title = r.Title
			}
			if r.Description != nil {
				if err = setText("description", *r.Description, &task.descriptionRef, MaxTaskTextBytes, false); err != nil {
					return Receipt{}, err
				}
			}
			if r.TicketRef != nil {
				if err = checkTicket(ctx, tx, r.SessionID, task.ID, *r.TicketRef); err != nil {
					return Receipt{}, err
				}
				task.TicketRef = *r.TicketRef
				change.TicketRef = r.TicketRef
			}
		case "assign", "unassign":
			if _, err = agentInSession(ctx, tx, r.AgentID, r.SessionID); err != nil {
				return Receipt{}, err
			}
			var exists int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM task_assignees WHERE task_id=? AND agent_id=?", task.ID, r.AgentID).Scan(&exists); err != nil {
				return Receipt{}, err
			}
			if (action == "assign" && exists != 0) || (action == "unassign" && exists == 0) {
				return Receipt{}, failure("conflict", "Task assignment already has the requested state")
			}
			if action == "assign" {
				_, err = tx.ExecContext(ctx, "INSERT INTO task_assignees VALUES(?,?,?,?,?)", task.ID, r.AgentID, p.SubjectID, now(), r.SessionID)
			} else {
				_, err = tx.ExecContext(ctx, "DELETE FROM task_assignees WHERE task_id=? AND agent_id=?", task.ID, r.AgentID)
				if err == nil {
					err = suppressTaskMentions(ctx, tx, task.ID, r.AgentID)
				}
			}
			if err != nil {
				return Receipt{}, err
			}
			change.AgentID = r.AgentID
		case "status":
			if task.Progress == "finished" || task.Progress == "cancelled" {
				return Receipt{}, failure("conflict", "reopen the Task before another progress update")
			}
			switch r.Progress {
			case "waiting":
				if r.Summary != "" {
					return Receipt{}, failure("invalid_input", "summary applies only to whole-Task completion")
				}
				err = setText("reason", r.Reason, &task.reasonRef, MaxTaskNoteBytes, true)
			case "working":
				if r.Summary != "" {
					return Receipt{}, failure("invalid_input", "summary applies only to whole-Task completion")
				}
				err = setText("reason", r.Reason, &task.reasonRef, MaxTaskNoteBytes, false)
			case "finished":
				if r.Reason != "" {
					return Receipt{}, failure("invalid_input", "completion uses a result summary")
				}
				err = setText("result", r.Summary, &task.resultRef, MaxTaskNoteBytes, true)
				task.reasonRef = ""
			case "cancelled":
				if r.Summary != "" {
					return Receipt{}, failure("invalid_input", "cancellation uses a reason")
				}
				err = setText("reason", r.Reason, &task.reasonRef, MaxTaskNoteBytes, true)
			default:
				return Receipt{}, failure("invalid_input", "unknown Task progress")
			}
			if err != nil {
				return Receipt{}, err
			}
			task.Progress = r.Progress
			change.Progress = r.Progress
		case "reopen":
			if task.Progress != "finished" && task.Progress != "cancelled" {
				return Receipt{}, failure("conflict", "only Finished or Cancelled Tasks can be reopened")
			}
			if err = setText("reason", r.Reason, &task.reasonRef, MaxTaskNoteBytes, true); err != nil {
				return Receipt{}, err
			}
			task.Progress = "waiting"
			change.Progress = "waiting"
		case "report":
			if r.Report == nil {
				return Receipt{}, failure("invalid_input", "report required")
			}
			if err = setText("report", *r.Report, &task.reportRef, MaxTaskTextBytes, false); err != nil {
				return Receipt{}, err
			}
			task.ReportAt = now()
		case "attention.set":
			if err = validText(r.Question, MaxTaskNoteBytes, true); err != nil {
				return Receipt{}, err
			}
			id := opaqueID()
			_, err = tx.ExecContext(ctx, "INSERT INTO task_questions(id,task_id,question,asker_id,asked_at) VALUES(?,?,?,?,?)", id, task.ID, r.Question, p.SubjectID, now())
			if err != nil {
				return Receipt{}, err
			}
			change.QuestionID = id
		case "attention.resolve":
			if err = validText(r.Note, MaxTaskNoteBytes, true); err != nil {
				return Receipt{}, err
			}
			question, err := openQuestion(ctx, tx, task.ID)
			if err != nil {
				return Receipt{}, err
			}
			if question == nil {
				return Receipt{}, failure("conflict", "Task has no open Operator question")
			}
			_, err = tx.ExecContext(ctx, "UPDATE task_questions SET note=?,resolver_id=?,resolved_at=? WHERE id=?", r.Note, p.SubjectID, now(), question.ID)
			if err != nil {
				return Receipt{}, err
			}
			change.QuestionID = question.ID
		case "reference.add":
			if err = validText(r.Location, 2048, true); err != nil {
				return Receipt{}, err
			}
			if err = validText(r.ReferenceRevision, 128, false); err != nil {
				return Receipt{}, err
			}
			lower := strings.ToLower(strings.TrimSpace(r.Location))
			for _, prefix := range []string{"javascript:", "data:", "vbscript:"} {
				if strings.HasPrefix(lower, prefix) {
					return Receipt{}, failure("invalid_input", "executable reference URLs are forbidden")
				}
			}
			var exists int
			if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM task_references WHERE task_id=? AND location=? AND revision=? AND removed_at IS NULL", task.ID, r.Location, r.ReferenceRevision).Scan(&exists); err != nil {
				return Receipt{}, err
			}
			if exists != 0 {
				return Receipt{}, failure("conflict", "reference already exists")
			}
			id := opaqueID()
			_, err = tx.ExecContext(ctx, "INSERT INTO task_references(id,task_id,location,revision,added_by,added_at) VALUES(?,?,?,?,?,?)", id, task.ID, r.Location, r.ReferenceRevision, p.SubjectID, now())
			if err != nil {
				return Receipt{}, err
			}
			change.ReferenceID = id
		case "reference.remove":
			if !validToken(r.ReferenceID) {
				return Receipt{}, failure("invalid_input", "Reference ID required")
			}
			result, err := tx.ExecContext(ctx, "UPDATE task_references SET removed_by=?,removed_at=? WHERE id=? AND task_id=? AND removed_at IS NULL", p.SubjectID, now(), r.ReferenceID, task.ID)
			if err != nil {
				return Receipt{}, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return Receipt{}, err
			}
			if n != 1 {
				return Receipt{}, failure("not_found", "active reference not found in Task")
			}
			change.ReferenceID = r.ReferenceID
		}
		nullable := func(ref string) any {
			if ref == "" {
				return nil
			}
			return ref
		}
		_, err = tx.ExecContext(ctx, "UPDATE tasks SET title=?,ticket_ref=?,description_ref=?,report_ref=?,reason_ref=?,result_ref=?,progress=?,revision=revision+1,updated_by=?,updated_at=?,report_at=? WHERE id=?", task.Title, task.TicketRef, nullable(task.descriptionRef), nullable(task.reportRef), nullable(task.reasonRef), nullable(task.resultRef), task.Progress, p.SubjectID, now(), task.ReportAt, task.ID)
		if err != nil {
			return Receipt{}, err
		}
		if err = recordTask(ctx, tx, p, task.ID, "task."+action, task.Revision+1, change); err != nil {
			return Receipt{}, err
		}
		if err = bumpSessionRead(ctx, tx, r.SessionID); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: task.ID, Revision: task.Revision + 1, Outcome: outcome}, nil
	})
}
func openQuestion(ctx context.Context, tx *sql.Tx, id string) (*Question, error) {
	var q Question
	err := tx.QueryRowContext(ctx, "SELECT id,task_id,question,asker_id,asked_at FROM task_questions WHERE task_id=? AND resolved_at IS NULL", id).Scan(&q.ID, &q.TaskID, &q.Question, &q.AskerID, &q.AskedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}
func (s *Store) ChangeDependency(ctx context.Context, token, action string, r DependencyRequest) (Receipt, error) {
	if action != "add" && action != "remove" {
		return Receipt{}, failure("invalid_input", "unknown dependency action")
	}
	return s.Mutate(ctx, token, operation(r.OperationID, "dependency."+action, r.SessionID, r), func(ctx context.Context, tx *sql.Tx, p Principal, receipt Receipt) error {
		_, err := ReadSession(ctx, tx, p, r.Context)
		return err
	}, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		session, err := OpenSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		if err = RequireOrchestrator(p, session); err != nil {
			return Receipt{}, err
		}
		if err = expectedRevision(r.ExpectedGraphRevision, session.GraphRevision); err != nil {
			return Receipt{}, err
		}
		if _, err = taskInSession(ctx, tx, r.PrerequisiteID, r.SessionID); err != nil {
			return Receipt{}, err
		}
		if _, err = taskInSession(ctx, tx, r.DependentID, r.SessionID); err != nil {
			return Receipt{}, err
		}
		if r.PrerequisiteID == r.DependentID {
			return Receipt{}, failure("invalid_input", "dependency cannot point to itself")
		}
		var id string
		err = tx.QueryRowContext(ctx, "SELECT id FROM dependencies WHERE session_id=? AND prerequisite_id=? AND dependent_id=?", r.SessionID, r.PrerequisiteID, r.DependentID).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Receipt{}, err
		}
		if action == "add" {
			if err == nil {
				return Receipt{}, failure("conflict", "dependency already exists")
			}
			var cycle int
			err = tx.QueryRowContext(ctx, `WITH RECURSIVE reachable(id) AS (SELECT ? UNION SELECT d.dependent_id FROM dependencies d JOIN reachable p ON d.prerequisite_id=p.id WHERE d.session_id=?) SELECT count(*) FROM reachable WHERE id=?`, r.DependentID, r.SessionID, r.PrerequisiteID).Scan(&cycle)
			if err != nil {
				return Receipt{}, err
			}
			if cycle != 0 {
				return Receipt{}, failure("conflict", "dependency would create a cycle")
			}
			id = opaqueID()
			_, err = tx.ExecContext(ctx, "INSERT INTO dependencies VALUES(?,?,?,?,?,?)", id, r.SessionID, r.PrerequisiteID, r.DependentID, p.SubjectID, now())
		} else {
			if errors.Is(err, sql.ErrNoRows) {
				return Receipt{}, failure("not_found", "dependency not found")
			}
			_, err = tx.ExecContext(ctx, "DELETE FROM dependencies WHERE id=?", id)
		}
		if err != nil {
			return Receipt{}, err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO graph_history(session_id,actor_id,action,edge_id,prerequisite_id,dependent_id,revision,received_at) VALUES(?,?,?,?,?,?,?,?)", r.SessionID, p.SubjectID, action, id, r.PrerequisiteID, r.DependentID, session.GraphRevision+1, now())
		if err != nil {
			return Receipt{}, err
		}
		if err = bumpGraph(ctx, tx, r.SessionID); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: id, Revision: session.GraphRevision + 1, Outcome: action}, nil
	})
}
