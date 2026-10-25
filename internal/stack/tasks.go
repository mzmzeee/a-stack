package stack

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
// LastMailAt is the time of the latest Task mail. Quiet marks a Working Task with no change and no Task mail
// for QuietAfter that does not wait for the Operator's answer (see QuietAt).
type Task struct {
	ReportPreview                                   string `json:"report_preview,omitempty"`
	QuestionPreview                                 string `json:"question_preview,omitempty"`
	LastAction                                      string `json:"last_action,omitempty"`
	MailCount                                       int64  `json:"mail_count"`
	LastMailAt                                      string `json:"last_mail_at,omitempty"`
	Quiet                                           bool   `json:"quiet"`
	Archived                                        bool   `json:"archived"`
	ArchivedReason                                  string `json:"archived_reason,omitempty"`
	ArchivedAt                                      string `json:"archived_at,omitempty"`
	EndedAt                                         string `json:"ended_at,omitempty"`
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
	chainDone                                       bool
}
type Question struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	Question string `json:"question"`
	AskerID  string `json:"asker_id"`
	AskedAt  string `json:"asked_at"`
	Note     string `json:"note,omitempty"`
	// Answer is the Operator's answer. A question that the Operator answered
	// has no resolver; its answer is stored as the note.
	Answer     string `json:"answer,omitempty"`
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
	// AnsweredQuestion is the latest question that the Operator answered.
	AnsweredQuestion *Question `json:"answered_question,omitempty"`
}
type TaskCreateRequest struct {
	Context
	OperationID string `json:"operation_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	TicketRef   string `json:"ticket_ref,omitempty"`
	// PrerequisiteIDs and AgentIDs link and assign the new Task in the same
	// operation. Nobody holds the new Task's revision, and a Task without
	// dependents cannot close a cycle, so they need no expected revision.
	PrerequisiteIDs []string `json:"prerequisite_ids,omitempty"`
	AgentIDs        []string `json:"agent_ids,omitempty"`
}

// MaxCreateLinks bounds the prerequisites, and the assignees, of one Task create.
const MaxCreateLinks = 100

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
	ID         string `json:"id,omitempty"` // empty for a text without a row; see createTexts
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

// cycleCheckSQL counts the new prerequisite among the Tasks reachable from the
// new dependent. CROSS JOIN fixes the join order without relying on table
// statistics: each recursive step searches dependencies_from for the edges
// out of one reached Task, instead of scanning every edge of the Session.
const cycleCheckSQL = `WITH RECURSIVE reachable(id) AS (SELECT ? UNION SELECT d.dependent_id FROM reachable p CROSS JOIN dependencies d WHERE d.session_id=? AND d.prerequisite_id=p.id) SELECT count(*) FROM reachable WHERE id=?`

const taskColumns = `t.id,t.session_id,t.title,t.ticket_ref,t.progress,t.revision,t.created_by,t.updated_by,t.created_at,t.updated_at,t.report_at,COALESCE(t.description_ref,''),COALESCE(t.report_ref,''),COALESCE(t.reason_ref,''),COALESCE(t.result_ref,''),
 (SELECT count(*) FROM task_assignees a WHERE a.task_id=t.id),EXISTS(SELECT 1 FROM task_questions q WHERE q.task_id=t.id AND q.resolved_at IS NULL),
 (SELECT count(*) FROM task_references r WHERE r.task_id=t.id AND r.removed_at IS NULL),
 (SELECT count(*) FROM dependencies d WHERE d.session_id=t.session_id AND d.dependent_id=t.id),
 (SELECT count(*) FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id WHERE d.session_id=t.session_id AND d.dependent_id=t.id AND p.progress='cancelled'),
 COALESCE((SELECT substr(c.body,1,160) FROM task_content c WHERE c.id=t.report_ref),''),
 COALESCE((SELECT substr(q.question,1,160) FROM task_questions q WHERE q.task_id=t.id AND q.resolved_at IS NULL),''),
 COALESCE((SELECT h.action FROM task_history h WHERE h.task_id=t.id ORDER BY h.sequence DESC LIMIT 1),''),
 COALESCE((SELECT max(m.sequence) FROM mail_messages m WHERE m.task_id=t.id),0),
 COALESCE((SELECT m.received_at FROM mail_messages m WHERE m.task_id=t.id ORDER BY m.sequence DESC LIMIT 1),''),
 t.archived_at,t.ended_at,
 CASE WHEN t.progress IN ('finished','cancelled') THEN EXISTS(SELECT 1 FROM dependencies d WHERE d.session_id=t.session_id AND d.prerequisite_id=t.id)
  AND NOT EXISTS(SELECT 1 FROM dependencies d JOIN tasks n ON n.id=d.dependent_id WHERE d.session_id=t.session_id AND d.prerequisite_id=t.id AND n.progress NOT IN ('finished','cancelled')) ELSE 0 END`

// Mail is append-only with sequences 1..n, so the last sequence is mail_count.
// The last column is the chain rule of deriveArchive: a done Task whose
// dependents are all done.

const taskByID = "SELECT " + taskColumns + " FROM tasks t WHERE t.id=? AND t.session_id=?"

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.SessionID, &t.Title, &t.TicketRef, &t.Progress, &t.Revision, &t.CreatedBy, &t.UpdatedBy, &t.CreatedAt, &t.UpdatedAt, &t.ReportAt, &t.descriptionRef, &t.reportRef, &t.reasonRef, &t.resultRef, &t.AssigneeCount, &t.NeedsYou, &t.ReferenceCount, &t.PrerequisiteCount, &t.CancelledPrerequisiteCount, &t.ReportPreview, &t.QuestionPreview, &t.LastAction, &t.MailCount, &t.LastMailAt, &t.ArchivedAt, &t.EndedAt, &t.chainDone)
	t.Unassigned = t.AssigneeCount == 0
	at := clockNow()
	deriveArchive(&t, at)
	t.Quiet = t.quiet(at)
	if errors.Is(err, sql.ErrNoRows) {
		return t, failure("not_found", "Task not found in Session")
	}
	if err != nil {
		return t, storageError(err)
	}
	return t, nil
}
func taskInSession(ctx context.Context, tx *sql.Tx, id, session string) (Task, error) {
	if !validToken(id) {
		return Task{}, failure("invalid_input", "Task ID required")
	}
	return scanTask(tx.QueryRowContext(ctx, taskByID, id, session))
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
		return storageError(err)
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
			return storageError(err)
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

// validText checks one text field and names it in the error.
func validText(field, body string, max int, required bool) error {
	switch {
	case required && strings.TrimSpace(body) == "":
		return failure("invalid_input", field+" is required")
	case len(body) > max:
		return failure("invalid_input", fmt.Sprintf("%s exceeds %d bytes", field, max))
	case !utf8.ValidString(body):
		return failure("invalid_input", field+" is not valid UTF-8")
	}
	return nil
}
func insertContent(ctx context.Context, tx *sql.Tx, p Principal, taskID, kind, body string) (string, error) {
	id := opaqueID()
	_, err := tx.ExecContext(ctx, "INSERT INTO task_content VALUES(?,?,?,?,?,?)", id, taskID, kind, body, p.SubjectID, now())
	return id, err
}

// NotStarted is the reason of a Task that no assignee has started yet.
const NotStarted = "Not started"

// defaultText is the text of a Task that has no row for it: a new Task stores
// no row for an empty description or for its starting reason. ok is false when
// a missing row means that the Task has no such text.
func defaultText(kind, progress string) (text string, ok bool) {
	switch {
	case kind == "description":
		return "", true
	case kind == "reason" && progress == "waiting":
		// Only a new Task is Waiting without a reason: every later change to
		// Waiting needs one.
		return NotStarted, true
	}
	return "", false
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
	// A view token acts for the Operator: its one write is the Operator's answer.
	kind, actor := "agent", any(p.SubjectID)
	if p.Kind != AgentCapability {
		kind, actor = "operator", nil
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO task_history(task_id,actor_kind,actor_id,action,revision,received_at,changes) VALUES(?,?,?,?,?,?,?)", taskID, kind, actor, action, revision, now(), string(b))
	return err
}
func bumpGraph(ctx context.Context, tx *sql.Tx, session string) error {
	_, err := tx.ExecContext(ctx, "UPDATE sessions SET graph_revision=graph_revision+1,read_revision=read_revision+1 WHERE id=?", session)
	return err
}
func checkTicket(ctx context.Context, tx *sql.Tx, session, task, ticket string) error {
	if err := validText("ticket_ref", ticket, 1024, false); err != nil {
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
		if err = validText("title", r.Title, 256, true); err != nil {
			return Receipt{}, err
		}
		if err = validText("description", r.Description, MaxTaskTextBytes, false); err != nil {
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
		// The starting reason and an empty description have no text row; see
		// defaultText.
		var texts []string
		if r.Description != "" {
			description, err := insertContent(ctx, tx, p, id, "description", r.Description)
			if err != nil {
				return Receipt{}, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE tasks SET description_ref=? WHERE id=?", description, id); err != nil {
				return Receipt{}, err
			}
			texts = append(texts, description)
		}
		if err = recordTask(ctx, tx, p, id, "task.create", 1, TaskChange{Title: &r.Title, TicketRef: &r.TicketRef, TextIDs: texts, Progress: "waiting"}); err != nil {
			return Receipt{}, err
		}
		revision, err := linkNewTask(ctx, tx, p, session, id, r)
		if err != nil {
			return Receipt{}, err
		}
		if err = bumpGraph(ctx, tx, r.SessionID); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: id, Revision: revision, Outcome: "created"}, nil
	})
}

// linkNewTask adds the prerequisites and the assignees of a new Task, as
// dependency add and task assign would, and returns the Task's revision. The
// links take the graph revision of the create; each assignment is one Task
// revision with its own history entry.
func linkNewTask(ctx context.Context, tx *sql.Tx, p Principal, session Session, id string, r TaskCreateRequest) (int64, error) {
	if len(r.PrerequisiteIDs) > MaxCreateLinks || len(r.AgentIDs) > MaxCreateLinks {
		return 0, failure("invalid_input", fmt.Sprintf("at most %d prerequisites and %d assignees per Task create", MaxCreateLinks, MaxCreateLinks))
	}
	timestamp := now()
	for i, prerequisite := range r.PrerequisiteIDs {
		if slices.Contains(r.PrerequisiteIDs[:i], prerequisite) {
			return 0, failure("invalid_input", "prerequisite "+prerequisite+" is given twice")
		}
		if _, err := taskInSession(ctx, tx, prerequisite, r.SessionID); err != nil {
			if e, ok := err.(*Error); ok && e.Code == "not_found" {
				return 0, failure("not_found", "prerequisite "+prerequisite+" is not a Task in this Session")
			}
			return 0, err
		}
		edge := opaqueID()
		if _, err := tx.ExecContext(ctx, "INSERT INTO dependencies VALUES(?,?,?,?,?,?)", edge, r.SessionID, prerequisite, id, p.SubjectID, timestamp); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO graph_history(session_id,actor_id,action,edge_id,prerequisite_id,dependent_id,revision,received_at) VALUES(?,?,?,?,?,?,?,?)", r.SessionID, p.SubjectID, "add", edge, prerequisite, id, session.GraphRevision+1, timestamp); err != nil {
			return 0, err
		}
	}
	revision := int64(1)
	for i, agent := range r.AgentIDs {
		if slices.Contains(r.AgentIDs[:i], agent) {
			return 0, failure("invalid_input", "Agent "+agent+" is given twice")
		}
		if _, err := agentInSession(ctx, tx, agent, r.SessionID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO task_assignees VALUES(?,?,?,?,?)", id, agent, p.SubjectID, timestamp, r.SessionID); err != nil {
			return 0, err
		}
		revision++
		if err := recordTask(ctx, tx, p, id, "task.assign", revision, TaskChange{AgentID: agent}); err != nil {
			return 0, err
		}
	}
	if revision > 1 {
		if _, err := tx.ExecContext(ctx, "UPDATE tasks SET revision=? WHERE id=?", revision, id); err != nil {
			return 0, err
		}
	}
	return revision, nil
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
	case "archive", "unarchive":
		allowed = ""
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
	archiving := action == "archive" || action == "unarchive"
	assigned := action != "edit" && action != "assign" && action != "unassign" && !archiving
	return s.Mutate(ctx, token, operation(r.OperationID, "task."+action, r.SessionID, r), replayTask(assigned), func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		session, err := OpenSession(ctx, tx, p, r.Context)
		if err != nil {
			return Receipt{}, err
		}
		task, err := taskInSession(ctx, tx, r.TaskID, r.SessionID)
		if err != nil {
			return Receipt{}, err
		}
		switch {
		case assigned:
			err = RequireTaskAssignee(ctx, tx, p, task)
		case archiving && p.Kind == OperatorCapability:
		default:
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
		e := &taskEdit{ctx: ctx, tx: tx, p: p, r: r, action: action, task: task}
		if err = taskActions[action](e); err != nil {
			return Receipt{}, err
		}
		task = e.task
		nullable := func(ref string) any {
			if ref == "" {
				return nil
			}
			return ref
		}
		// updated_by names an Agent. An Operator archive keeps the last Agent; the
		// history entry records the Operator.
		updatedBy := p.SubjectID
		if p.Kind == OperatorCapability {
			updatedBy = task.UpdatedBy
		}
		_, err = tx.ExecContext(ctx, "UPDATE tasks SET title=?,ticket_ref=?,description_ref=?,report_ref=?,reason_ref=?,result_ref=?,progress=?,revision=revision+1,updated_by=?,updated_at=?,report_at=?,archived_at=?,ended_at=? WHERE id=?", task.Title, task.TicketRef, nullable(task.descriptionRef), nullable(task.reportRef), nullable(task.reasonRef), nullable(task.resultRef), task.Progress, updatedBy, now(), task.ReportAt, task.ArchivedAt, task.EndedAt, task.ID)
		if err != nil {
			return Receipt{}, err
		}
		if err = recordTask(ctx, tx, p, task.ID, "task."+action, task.Revision+1, e.change); err != nil {
			return Receipt{}, err
		}
		if err = bumpSessionRead(ctx, tx, r.SessionID); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: task.ID, Revision: task.Revision + 1, Outcome: action}, nil
	})
}

// taskEdit is one Task change while its action runs. The action changes task
// and change; ChangeTask then writes the Task row and the history entry.
type taskEdit struct {
	ctx    context.Context
	tx     *sql.Tx
	p      Principal
	r      TaskChangeRequest
	action string
	task   Task
	change TaskChange
}

// taskActions has one function for each action that taskChangeFields accepts.
var taskActions = map[string]func(*taskEdit) error{
	"edit":              (*taskEdit).edit,
	"assign":            (*taskEdit).assignment,
	"unassign":          (*taskEdit).assignment,
	"status":            (*taskEdit).status,
	"reopen":            (*taskEdit).reopen,
	"archive":           (*taskEdit).archive,
	"unarchive":         (*taskEdit).unarchive,
	"report":            (*taskEdit).report,
	"attention.set":     (*taskEdit).ask,
	"attention.resolve": (*taskEdit).resolve,
	"reference.add":     (*taskEdit).addReference,
	"reference.remove":  (*taskEdit).removeReference,
}

// setText stores body as a new text version of kind, unless it equals the
// current text, and points current at it.
func (e *taskEdit) setText(kind, body string, current *string, max int, required bool) error {
	field := kind
	if kind == "result" {
		field = "summary"
	}
	if err := validText(field, body, max, required); err != nil {
		return err
	}
	old, stored := defaultText(kind, e.task.Progress)
	if *current != "" {
		var err error
		if old, err = contentBody(e.ctx, e.tx, *current); err != nil {
			return err
		}
		stored = true
	}
	if stored && old == body {
		return nil
	}
	id, err := insertContent(e.ctx, e.tx, e.p, e.task.ID, kind, body)
	if err != nil {
		return err
	}
	*current = id
	e.change.TextIDs = append(e.change.TextIDs, id)
	return nil
}
func (e *taskEdit) edit() error {
	r := e.r
	if r.Title == nil && r.Description == nil && r.TicketRef == nil {
		return failure("invalid_input", "at least one definition field required")
	}
	if r.Title != nil {
		if err := validText("title", *r.Title, 256, true); err != nil {
			return err
		}
		e.task.Title = *r.Title
		e.change.Title = r.Title
	}
	if r.Description != nil {
		if err := e.setText("description", *r.Description, &e.task.descriptionRef, MaxTaskTextBytes, false); err != nil {
			return err
		}
	}
	if r.TicketRef != nil {
		if err := checkTicket(e.ctx, e.tx, r.SessionID, e.task.ID, *r.TicketRef); err != nil {
			return err
		}
		e.task.TicketRef = *r.TicketRef
		e.change.TicketRef = r.TicketRef
	}
	return nil
}
func (e *taskEdit) assignment() error {
	ctx, tx, r := e.ctx, e.tx, e.r
	if _, err := agentInSession(ctx, tx, r.AgentID, r.SessionID); err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM task_assignees WHERE task_id=? AND agent_id=?", e.task.ID, r.AgentID).Scan(&exists); err != nil {
		return err
	}
	if (e.action == "assign" && exists != 0) || (e.action == "unassign" && exists == 0) {
		return failure("conflict", "Task assignment already has the requested state")
	}
	var err error
	if e.action == "assign" {
		_, err = tx.ExecContext(ctx, "INSERT INTO task_assignees VALUES(?,?,?,?,?)", e.task.ID, r.AgentID, e.p.SubjectID, now(), r.SessionID)
	} else {
		_, err = tx.ExecContext(ctx, "DELETE FROM task_assignees WHERE task_id=? AND agent_id=?", e.task.ID, r.AgentID)
		if err == nil {
			err = suppressTaskMentions(ctx, tx, e.task.ID, r.AgentID)
		}
	}
	if err != nil {
		return err
	}
	e.change.AgentID = r.AgentID
	return nil
}
func (e *taskEdit) status() error {
	r, task := e.r, &e.task
	if task.Progress == "finished" || task.Progress == "cancelled" {
		return failure("conflict", "reopen the Task before another progress update")
	}
	var err error
	switch r.Progress {
	case "waiting":
		if r.Summary != "" {
			return failure("invalid_input", "summary applies only to whole-Task completion")
		}
		err = e.setText("reason", r.Reason, &task.reasonRef, MaxTaskNoteBytes, true)
	case "working":
		if r.Summary != "" {
			return failure("invalid_input", "summary applies only to whole-Task completion")
		}
		err = e.setText("reason", r.Reason, &task.reasonRef, MaxTaskNoteBytes, false)
	case "finished":
		if r.Reason != "" {
			return failure("invalid_input", "completion uses a result summary")
		}
		err = e.setText("result", r.Summary, &task.resultRef, MaxTaskNoteBytes, true)
		task.reasonRef = ""
	case "cancelled":
		if r.Summary != "" {
			return failure("invalid_input", "cancellation uses a reason")
		}
		err = e.setText("reason", r.Reason, &task.reasonRef, MaxTaskNoteBytes, true)
	default:
		return failure("invalid_input", "unknown Task progress")
	}
	if err != nil {
		return err
	}
	task.Progress = r.Progress
	e.change.Progress = r.Progress
	if r.Progress == "finished" || r.Progress == "cancelled" {
		task.EndedAt = now()
	}
	return nil
}
func (e *taskEdit) reopen() error {
	task := &e.task
	if task.Progress != "finished" && task.Progress != "cancelled" {
		return failure("conflict", "only Finished or Cancelled Tasks can be reopened")
	}
	if err := e.setText("reason", e.r.Reason, &task.reasonRef, MaxTaskNoteBytes, true); err != nil {
		return err
	}
	task.Progress = "waiting"
	e.change.Progress = "waiting"
	task.ArchivedAt, task.EndedAt = "", ""
	return nil
}
func (e *taskEdit) archive() error {
	if e.task.Progress != "finished" && e.task.Progress != "cancelled" {
		return failure("conflict", "only Finished or Cancelled Tasks can be archived")
	}
	if e.task.ArchivedAt != "" {
		return failure("conflict", "Task is already archived")
	}
	e.task.ArchivedAt = now()
	return nil
}
func (e *taskEdit) unarchive() error {
	if e.task.ArchivedAt == "" {
		return failure("conflict", "Task was not archived with task archive")
	}
	e.task.ArchivedAt = ""
	return nil
}
func (e *taskEdit) report() error {
	if e.r.Report == nil {
		return failure("invalid_input", "report required")
	}
	if err := e.setText("report", *e.r.Report, &e.task.reportRef, MaxTaskTextBytes, false); err != nil {
		return err
	}
	e.task.ReportAt = now()
	return nil
}
func (e *taskEdit) ask() error {
	if err := validText("question", e.r.Question, MaxTaskNoteBytes, true); err != nil {
		return err
	}
	id := opaqueID()
	if _, err := e.tx.ExecContext(e.ctx, "INSERT INTO task_questions(id,task_id,question,asker_id,asked_at) VALUES(?,?,?,?,?)", id, e.task.ID, e.r.Question, e.p.SubjectID, now()); err != nil {
		return err
	}
	e.change.QuestionID = id
	return nil
}
func (e *taskEdit) resolve() error {
	if err := validText("note", e.r.Note, MaxTaskNoteBytes, true); err != nil {
		return err
	}
	question, err := openQuestion(e.ctx, e.tx, e.task.ID)
	if err != nil {
		return err
	}
	if question == nil {
		return failure("conflict", "Task has no open Operator question")
	}
	if _, err = e.tx.ExecContext(e.ctx, "UPDATE task_questions SET note=?,resolver_id=?,resolved_at=? WHERE id=?", e.r.Note, e.p.SubjectID, now(), question.ID); err != nil {
		return err
	}
	e.change.QuestionID = question.ID
	return nil
}
func (e *taskEdit) addReference() error {
	r := e.r
	if err := validText("location", r.Location, 2048, true); err != nil {
		return err
	}
	if err := validText("reference_revision", r.ReferenceRevision, 128, false); err != nil {
		return err
	}
	lower := strings.ToLower(strings.TrimSpace(r.Location))
	for _, prefix := range []string{"javascript:", "data:", "vbscript:"} {
		if strings.HasPrefix(lower, prefix) {
			return failure("invalid_input", "executable reference URLs are forbidden")
		}
	}
	var exists int
	if err := e.tx.QueryRowContext(e.ctx, "SELECT count(*) FROM task_references WHERE task_id=? AND location=? AND revision=? AND removed_at IS NULL", e.task.ID, r.Location, r.ReferenceRevision).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return failure("conflict", "reference already exists")
	}
	id := opaqueID()
	if _, err := e.tx.ExecContext(e.ctx, "INSERT INTO task_references(id,task_id,location,revision,added_by,added_at) VALUES(?,?,?,?,?,?)", id, e.task.ID, r.Location, r.ReferenceRevision, e.p.SubjectID, now()); err != nil {
		return err
	}
	e.change.ReferenceID = id
	return nil
}
func (e *taskEdit) removeReference() error {
	if !validToken(e.r.ReferenceID) {
		return failure("invalid_input", "Reference ID required")
	}
	result, err := e.tx.ExecContext(e.ctx, "UPDATE task_references SET removed_by=?,removed_at=? WHERE id=? AND task_id=? AND removed_at IS NULL", e.p.SubjectID, now(), e.r.ReferenceID, e.task.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return failure("not_found", "active reference not found in Task")
	}
	e.change.ReferenceID = e.r.ReferenceID
	return nil
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

const questionColumns = "id,task_id,question,asker_id,asked_at,note,COALESCE(resolver_id,''),COALESCE(resolved_at,'')"

// scanQuestion reads questionColumns. It returns nil without an error when
// there is no row.
func scanQuestion(row *sql.Row) (*Question, error) {
	var q Question
	err := row.Scan(&q.ID, &q.TaskID, &q.Question, &q.AskerID, &q.AskedAt, &q.Note, &q.ResolverID, &q.ResolvedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if q.ResolvedAt != "" && q.ResolverID == "" {
		q.Answer, q.Note = q.Note, ""
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
			err = tx.QueryRowContext(ctx, cycleCheckSQL, r.DependentID, r.SessionID, r.PrerequisiteID).Scan(&cycle)
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
