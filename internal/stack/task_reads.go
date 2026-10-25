package stack

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
)

const readPageBudget = 768 << 10

type SnapshotRequest struct {
	Context
	KnownRevision int64 `json:"known_revision,omitempty"`
	// knownMark is the archive mark of the caller's snapshot ETag, when it
	// sent one; -1 for an ETag without one.
	knownMark    int64
	hasKnownMark bool
}
type SnapshotCounts struct {
	Tasks        int `json:"tasks"`
	Agents       int `json:"agents"`
	Dependencies int `json:"dependencies"`
	Assignments  int `json:"assignments"`
}
type SessionSnapshot struct {
	Source     string  `json:"source"`
	CapturedAt string  `json:"captured_at"`
	Session    Session `json:"session"`
	// WorkspaceName is the last element of the Workspace path, never the full path.
	WorkspaceName string          `json:"workspace_name"`
	Unchanged     bool            `json:"unchanged"`
	Counts        *SnapshotCounts `json:"counts,omitempty"`
	archiveMark   int64           // in the ETag; see Store.archiveMark
}
type TaskReadRequest struct {
	Context
	TaskID       string `json:"task_id"`
	ReadRevision int64  `json:"read_revision,omitempty"`
}
type SnapshotPageRequest struct {
	Context
	Kind         string `json:"kind"`
	TaskID       string `json:"task_id,omitempty"`
	ReadRevision int64  `json:"read_revision,omitempty"`
	After        string `json:"after,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}
type SnapshotPage struct {
	ReadRevision int64  `json:"read_revision"`
	Items        []any  `json:"items"`
	Next         string `json:"next,omitempty"`
}
type SnapshotAgent struct {
	Agent
	RuntimeSource   string `json:"runtime_source"`
	RuntimeVerified bool   `json:"runtime_verified"`
}
type Assignment struct {
	TaskID    string `json:"task_id"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	AddedBy   string `json:"added_by"`
	AddedAt   string `json:"added_at"`
}
type TaskHistoryEntry struct {
	Sequence   int64          `json:"sequence"`
	TaskID     string         `json:"task_id"`
	ActorKind  string         `json:"actor_kind"`
	ActorID    string         `json:"actor_id,omitempty"`
	Action     string         `json:"action"`
	Revision   int64          `json:"revision"`
	ReceivedAt string         `json:"received_at"`
	Changes    TaskChange     `json:"changes"`
	Texts      []TaskContent  `json:"texts,omitempty"`
	Question   *Question      `json:"question,omitempty"`
	Reference  *FileReference `json:"reference,omitempty"`
}
type GraphHistoryEntry struct {
	Sequence       int64  `json:"sequence"`
	ActorID        string `json:"actor_id"`
	Action         string `json:"action"`
	EdgeID         string `json:"edge_id"`
	PrerequisiteID string `json:"prerequisite_id"`
	DependentID    string `json:"dependent_id"`
	Revision       int64  `json:"revision"`
	ReceivedAt     string `json:"received_at"`
}
type snapshotCursor struct {
	SessionID    string `json:"session_id"`
	TaskID       string `json:"task_id"`
	Kind         string `json:"kind"`
	ReadRevision int64  `json:"read_revision"`
	Position     string `json:"position"`
}

func pinReadRevision(expected int64, session Session) error {
	if expected < 0 {
		return failure("invalid_input", "read_revision must not be negative")
	}
	if expected != 0 && expected != session.ReadRevision {
		return &Error{Code: "conflict", Message: "Session read revision changed; restart the snapshot collection", CurrentRevision: &session.ReadRevision}
	}
	return nil
}
func (s *Store) readSessionTx(ctx context.Context, token string, ref Context) (*sql.Tx, Principal, Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, Principal{}, Session{}, storageError(err)
	}
	p, err := authenticate(ctx, tx, token)
	if err != nil {
		tx.Rollback()
		return nil, p, Session{}, err
	}
	session, err := ReadSession(ctx, tx, p, ref)
	if err != nil {
		tx.Rollback()
		return nil, p, session, err
	}
	return tx, p, session, nil
}
func (s *Store) Snapshot(ctx context.Context, token string, r SnapshotRequest) (SessionSnapshot, error) {
	tx, _, session, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return SessionSnapshot{}, err
	}
	defer tx.Rollback()
	if r.KnownRevision < 0 {
		return SessionSnapshot{}, failure("invalid_input", "known_revision must not be negative")
	}
	var workspacePath string
	if err = tx.QueryRowContext(ctx, `SELECT path FROM workspaces WHERE id=?`, session.WorkspaceID).Scan(&workspacePath); err != nil {
		return SessionSnapshot{}, storageError(err)
	}
	mark, err := s.archiveMark(ctx, tx, session)
	if err != nil {
		return SessionSnapshot{}, err
	}
	unchanged := r.KnownRevision == session.ReadRevision && (!r.hasKnownMark || r.knownMark == mark)
	snapshot := SessionSnapshot{Source: "local-service", CapturedAt: now(), Session: session, WorkspaceName: filepath.Base(workspacePath), Unchanged: unchanged, archiveMark: mark}
	if snapshot.Unchanged {
		return snapshot, nil
	}
	counts := &SnapshotCounts{}
	err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tasks WHERE session_id=?),(SELECT count(*) FROM agents WHERE session_id=?),(SELECT count(*) FROM dependencies WHERE session_id=?),(SELECT count(*) FROM task_assignees WHERE session_id=?)`, session.ID, session.ID, session.ID, session.ID).Scan(&counts.Tasks, &counts.Agents, &counts.Dependencies, &counts.Assignments)
	if err != nil {
		return SessionSnapshot{}, storageError(err)
	}
	snapshot.Counts = counts
	return snapshot, nil
}
func (s *Store) ShowTask(ctx context.Context, token string, r TaskReadRequest) (TaskDetail, error) {
	tx, _, session, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return TaskDetail{}, err
	}
	defer tx.Rollback()
	if err = pinReadRevision(r.ReadRevision, session); err != nil {
		return TaskDetail{}, err
	}
	task, err := taskInSession(ctx, tx, r.TaskID, r.SessionID)
	if err != nil {
		return TaskDetail{}, err
	}
	detail := TaskDetail{Task: task, ReadRevision: session.ReadRevision}
	for _, field := range []struct {
		ref    string
		target *string
		kind   string
	}{{task.descriptionRef, &detail.Description, "description"}, {task.reportRef, &detail.Report, "report"}, {task.reasonRef, &detail.Reason, "reason"}, {task.resultRef, &detail.Result, "result"}} {
		if field.ref == "" {
			*field.target, _ = defaultText(field.kind, task.Progress)
			continue
		}
		body, err := contentBody(ctx, tx, field.ref)
		if err != nil {
			return TaskDetail{}, storageError(err)
		}
		*field.target = body
	}
	detail.Question, err = openQuestion(ctx, tx, task.ID)
	if err != nil {
		return TaskDetail{}, storageError(err)
	}
	return detail, nil
}
func decodeSnapshotCursor(r *SnapshotPageRequest) (string, error) {
	if r.After == "" {
		return "", nil
	}
	if len(r.After) > 1024 {
		return "", failure("invalid_input", "invalid read cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(r.After)
	if err != nil {
		return "", failure("invalid_input", "invalid read cursor")
	}
	var c snapshotCursor
	if decodeJSON(b, &c) != nil || c.SessionID != r.SessionID || c.TaskID != r.TaskID || c.Kind != r.Kind || c.ReadRevision < 1 || (r.ReadRevision != 0 && r.ReadRevision != c.ReadRevision) {
		return "", failure("invalid_input", "cursor belongs to another read context")
	}
	r.ReadRevision = c.ReadRevision
	if r.Kind == "history" || r.Kind == "graph_history" {
		n, err := strconv.ParseInt(c.Position, 10, 64)
		if err != nil || n < 1 {
			return "", failure("invalid_input", "invalid history cursor")
		}
	} else if r.Kind == "assignments" {
		parts := strings.Split(c.Position, "/")
		if len(parts) != 2 || !validToken(parts[0]) || !validToken(parts[1]) || (r.TaskID != "" && parts[0] != r.TaskID) {
			return "", failure("invalid_input", "invalid assignment cursor")
		}
	} else if !validToken(c.Position) {
		return "", failure("invalid_input", "invalid entity cursor")
	}
	return c.Position, nil
}
func encodeSnapshotCursor(r SnapshotPageRequest, revision int64, position string) string {
	b, _ := json.Marshal(snapshotCursor{r.SessionID, r.TaskID, r.Kind, revision, position})
	return base64.RawURLEncoding.EncodeToString(b)
}
func readIDs(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, storageError(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return ids, nil
}
func (s *Store) SnapshotPage(ctx context.Context, token string, r SnapshotPageRequest) (SnapshotPage, error) {
	empty := SnapshotPage{}
	if r.Limit == 0 {
		r.Limit = 50
	}
	if r.Limit < 1 || r.Limit > 100 {
		return empty, failure("invalid_input", "page limit must be 1–100")
	}
	switch r.Kind {
	case "tasks", "agents", "dependencies", "assignments", "references", "history", "graph_history":
	default:
		return empty, failure("invalid_input", "unknown snapshot page kind")
	}
	if (r.Kind == "references" || r.Kind == "history") && r.TaskID == "" {
		return empty, failure("invalid_input", "Task ID required for detail history/references")
	}
	if r.TaskID != "" && r.Kind != "assignments" && r.Kind != "dependencies" && r.Kind != "references" && r.Kind != "history" {
		return empty, failure("invalid_input", "Task filter does not apply to this page")
	}
	position, err := decodeSnapshotCursor(&r)
	if err != nil {
		return empty, err
	}
	tx, _, session, err := s.readSessionTx(ctx, token, r.Context)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	if err = pinReadRevision(r.ReadRevision, session); err != nil {
		return empty, err
	}
	if r.TaskID != "" {
		if _, err = taskInSession(ctx, tx, r.TaskID, r.SessionID); err != nil {
			return empty, err
		}
	}
	var ids []string
	switch r.Kind {
	case "tasks":
		ids, err = readIDs(ctx, tx, "SELECT id FROM tasks WHERE session_id=? AND id>? ORDER BY id LIMIT ?", r.SessionID, position, r.Limit+1)
	case "agents":
		ids, err = readIDs(ctx, tx, "SELECT id FROM agents WHERE session_id=? AND id>? ORDER BY id LIMIT ?", r.SessionID, position, r.Limit+1)
	case "dependencies":
		if r.TaskID == "" {
			ids, err = readIDs(ctx, tx, "SELECT id FROM dependencies WHERE session_id=? AND id>? ORDER BY id LIMIT ?", r.SessionID, position, r.Limit+1)
		} else {
			ids, err = readIDs(ctx, tx, "SELECT id FROM dependencies WHERE session_id=? AND dependent_id=? AND id>? ORDER BY id LIMIT ?", r.SessionID, r.TaskID, position, r.Limit+1)
		}
	case "assignments":
		first, second := "", ""
		if position != "" {
			parts := strings.Split(position, "/")
			first, second = parts[0], parts[1]
		}
		if r.TaskID == "" {
			ids, err = readIDs(ctx, tx, "SELECT task_id||'/'||agent_id FROM task_assignees WHERE session_id=? AND (task_id,agent_id)>(?,?) ORDER BY task_id,agent_id LIMIT ?", r.SessionID, first, second, r.Limit+1)
		} else {
			ids, err = readIDs(ctx, tx, "SELECT task_id||'/'||agent_id FROM task_assignees WHERE session_id=? AND task_id=? AND agent_id>? ORDER BY agent_id LIMIT ?", r.SessionID, r.TaskID, second, r.Limit+1)
		}
	case "references":
		ids, err = readIDs(ctx, tx, "SELECT id FROM task_references WHERE task_id=? AND removed_at IS NULL AND id>? ORDER BY id LIMIT ?", r.TaskID, position, r.Limit+1)
	case "history":
		if position == "" {
			position = "0"
		}
		ids, err = readIDs(ctx, tx, "SELECT sequence FROM task_history WHERE task_id=? AND sequence>? ORDER BY sequence LIMIT ?", r.TaskID, position, r.Limit+1)
	case "graph_history":
		if position == "" {
			position = "0"
		}
		ids, err = readIDs(ctx, tx, "SELECT sequence FROM graph_history WHERE session_id=? AND sequence>? ORDER BY sequence LIMIT ?", r.SessionID, position, r.Limit+1)
	}
	if err != nil {
		return empty, err
	}
	result := SnapshotPage{ReadRevision: session.ReadRevision, Items: []any{}}
	used := 2
	last := ""
	for _, id := range ids {
		if len(result.Items) == r.Limit {
			result.Next = encodeSnapshotCursor(r, session.ReadRevision, last)
			break
		}
		item, err := snapshotItem(ctx, tx, r, id)
		if err != nil {
			return empty, publicError(err)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return empty, storageError(err)
		}
		if used+len(encoded)+1 > readPageBudget {
			if len(result.Items) == 0 {
				return empty, failure("storage_unavailable", "stored item exceeds the bounded read budget")
			}
			result.Next = encodeSnapshotCursor(r, session.ReadRevision, last)
			break
		}
		used += len(encoded) + 1
		result.Items = append(result.Items, item)
		last = id
	}
	return result, nil
}
func snapshotItem(ctx context.Context, tx *sql.Tx, r SnapshotPageRequest, id string) (any, error) {
	switch r.Kind {
	case "tasks":
		return taskInSession(ctx, tx, id, r.SessionID)
	case "agents":
		agent, err := agentInSession(ctx, tx, id, r.SessionID)
		source := "not-observed"
		if agent.ObservedAt != "" {
			source = "caller-observation"
		}
		return SnapshotAgent{Agent: agent, RuntimeSource: source, RuntimeVerified: false}, err
	case "dependencies":
		var edge Dependency
		err := tx.QueryRowContext(ctx, "SELECT d.id,d.prerequisite_id,d.dependent_id,p.progress,n.progress,d.created_by,d.created_at FROM dependencies d JOIN tasks p ON p.id=d.prerequisite_id JOIN tasks n ON n.id=d.dependent_id WHERE d.id=? AND d.session_id=?", id, r.SessionID).Scan(&edge.ID, &edge.PrerequisiteID, &edge.DependentID, &edge.PrerequisiteProgress, &edge.DependentProgress, &edge.CreatedBy, &edge.CreatedAt)
		return edge, err
	case "assignments":
		parts := strings.Split(id, "/")
		var a Assignment
		err := tx.QueryRowContext(ctx, "SELECT m.task_id,m.agent_id,a.name,m.added_by,m.added_at FROM task_assignees m JOIN agents a ON a.id=m.agent_id WHERE m.task_id=? AND m.agent_id=? AND m.session_id=?", parts[0], parts[1], r.SessionID).Scan(&a.TaskID, &a.AgentID, &a.AgentName, &a.AddedBy, &a.AddedAt)
		return a, err
	case "references":
		return readReference(ctx, tx, r.TaskID, id)
	case "history":
		var h TaskHistoryEntry
		var change string
		err := tx.QueryRowContext(ctx, "SELECT sequence,task_id,actor_kind,COALESCE(actor_id,''),action,revision,received_at,changes FROM task_history WHERE sequence=? AND task_id=?", id, r.TaskID).Scan(&h.Sequence, &h.TaskID, &h.ActorKind, &h.ActorID, &h.Action, &h.Revision, &h.ReceivedAt, &change)
		if err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(change), &h.Changes) != nil {
			return nil, storageError()
		}
		for _, textID := range h.Changes.TextIDs {
			var text TaskContent
			if err = tx.QueryRowContext(ctx, "SELECT id,kind,body,actor_id,received_at FROM task_content WHERE id=? AND task_id=?", textID, r.TaskID).Scan(&text.ID, &text.Kind, &text.Body, &text.ActorID, &text.ReceivedAt); err != nil {
				return nil, err
			}
			h.Texts = append(h.Texts, text)
		}
		if h.Action == "task.create" {
			h.Texts = createTexts(h)
		}
		if h.Changes.QuestionID != "" {
			var q Question
			err = tx.QueryRowContext(ctx, "SELECT id,task_id,question,asker_id,asked_at,note,COALESCE(resolver_id,''),COALESCE(resolved_at,'') FROM task_questions WHERE id=? AND task_id=?", h.Changes.QuestionID, r.TaskID).Scan(&q.ID, &q.TaskID, &q.Question, &q.AskerID, &q.AskedAt, &q.Note, &q.ResolverID, &q.ResolvedAt)
			if err != nil {
				return nil, err
			}
			h.Question = &q
		}
		if h.Changes.ReferenceID != "" {
			ref, err := readReference(ctx, tx, r.TaskID, h.Changes.ReferenceID)
			if err != nil {
				return nil, err
			}
			h.Reference = &ref
		}
		return h, nil
	case "graph_history":
		var h GraphHistoryEntry
		err := tx.QueryRowContext(ctx, "SELECT sequence,actor_id,action,edge_id,prerequisite_id,dependent_id,revision,received_at FROM graph_history WHERE sequence=? AND session_id=?", id, r.SessionID).Scan(&h.Sequence, &h.ActorID, &h.Action, &h.EdgeID, &h.PrerequisiteID, &h.DependentID, &h.Revision, &h.ReceivedAt)
		return h, err
	}
	return nil, failure("invalid_input", "unknown read item")
}

// createTexts adds the texts that a new Task does not store (see defaultText)
// to its create entry, without an ID, in the order older entries list them.
func createTexts(h TaskHistoryEntry) []TaskContent {
	if len(h.Texts) == 2 {
		return h.Texts
	}
	texts := []TaskContent{{Kind: "description", ActorID: h.ActorID, ReceivedAt: h.ReceivedAt}}
	if len(h.Texts) == 1 {
		texts[0] = h.Texts[0]
	}
	return append(texts, TaskContent{Kind: "reason", Body: NotStarted, ActorID: h.ActorID, ReceivedAt: h.ReceivedAt})
}
func readReference(ctx context.Context, tx *sql.Tx, task, id string) (FileReference, error) {
	var ref FileReference
	err := tx.QueryRowContext(ctx, "SELECT id,task_id,location,revision,added_by,added_at,COALESCE(removed_by,''),COALESCE(removed_at,'') FROM task_references WHERE id=? AND task_id=?", id, task).Scan(&ref.ID, &ref.TaskID, &ref.Location, &ref.Revision, &ref.AddedBy, &ref.AddedAt, &ref.RemovedBy, &ref.RemovedAt)
	return ref, err
}
