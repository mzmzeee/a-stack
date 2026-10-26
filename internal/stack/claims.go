package stack

import (
	"context"
	"database/sql"
	"path"
	"runtime"
	"slices"
	"strings"
	"unicode"
)

const MaxTaskClaims = 100

type FileClaim struct {
	TaskID    string `json:"task_id"`
	Path      string `json:"path"`
	ClaimedBy string `json:"claimed_by"`
	ClaimedAt string `json:"claimed_at"`
}
type ClaimWarning struct {
	TaskID string   `json:"task_id"`
	Title  string   `json:"title"`
	Paths  []string `json:"paths"`
	Agents []string `json:"agents"`
}
type ClaimRequest struct {
	Context
	OperationID      string   `json:"operation_id"`
	TaskID           string   `json:"task_id"`
	ExpectedRevision int64    `json:"expected_revision"`
	Paths            []string `json:"paths"`
}
type ClaimResult struct {
	Receipt
	Warnings []ClaimWarning `json:"warnings"`
}
type TaskClaims struct {
	ReadRevision int64          `json:"read_revision"`
	Claims       []string       `json:"claims"`
	Warnings     []ClaimWarning `json:"warnings"`
}

// Claims are lexical metadata, never paths the service opens or resolves.
func claimPath(value string) (string, error) {
	if validText("claim path", value, 1024, true) != nil || strings.ContainsAny(value, ":*?") || strings.ContainsFunc(value, unicode.IsControl) {
		return "", failure("invalid_input", "claim path must be a literal file or folder relative to the Workspace root")
	}
	value = strings.ReplaceAll(value, `\`, "/")
	value = path.Clean(value)
	if strings.HasPrefix(value, "/") || value == ".." || strings.HasPrefix(value, "../") {
		return "", failure("invalid_input", "claim path must stay within the Workspace root")
	}
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value, nil
}

func (s *Store) ChangeClaims(ctx context.Context, token string, remove bool, r ClaimRequest) (ClaimResult, error) {
	result := ClaimResult{Warnings: []ClaimWarning{}}
	action := "claim"
	if remove {
		action = "unclaim"
	}
	// Keep the submitted paths in the operation digest, even when normalizing
	// them would produce the same claim.
	replay := func(ctx context.Context, tx *sql.Tx, p Principal, receipt Receipt) error {
		if err := replayTask(true)(ctx, tx, p, receipt); err != nil {
			return err
		}
		var err error
		result.Warnings, err = claimWarnings(ctx, tx, r.WorkspaceID, receipt.ResourceID)
		return err
	}
	receipt, err := s.Mutate(ctx, token, operation(r.OperationID, "task."+action, r.SessionID, r), replay, func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if _, err := OpenSession(ctx, tx, p, r.Context); err != nil {
			return Receipt{}, err
		}
		task, err := ReadAssignedTask(ctx, tx, p, r.Context, r.TaskID)
		if err != nil {
			return Receipt{}, err
		}
		if err = taskRevision(r.ExpectedRevision, task); err != nil {
			return Receipt{}, err
		}
		if task.Progress == "finished" || task.Progress == "cancelled" {
			return Receipt{}, failure("conflict", "reopen the Task before changing claims")
		}
		if len(r.Paths) == 0 || len(r.Paths) > MaxTaskClaims {
			return Receipt{}, failure("invalid_input", "give 1–100 claim paths")
		}
		paths := []string{}
		for _, value := range r.Paths {
			value, err = claimPath(value)
			if err != nil {
				return Receipt{}, err
			}
			if !slices.Contains(paths, value) {
				paths = append(paths, value)
			}
		}
		slices.Sort(paths)
		for _, value := range paths {
			if remove {
				_, err = tx.ExecContext(ctx, "DELETE FROM task_claims WHERE task_id=? AND path=?", task.ID, value)
			} else {
				_, err = tx.ExecContext(ctx, "INSERT INTO task_claims VALUES(?,?,?,?) ON CONFLICT(task_id,path) DO NOTHING", task.ID, value, p.SubjectID, now())
			}
			if err != nil {
				return Receipt{}, err
			}
		}
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM task_claims WHERE task_id=?", task.ID).Scan(&count); err != nil {
			return Receipt{}, err
		}
		if count > MaxTaskClaims {
			return Receipt{}, failure("invalid_input", "a Task can claim at most 100 paths")
		}
		if _, err = tx.ExecContext(ctx, "UPDATE tasks SET revision=revision+1,updated_by=?,updated_at=? WHERE id=?", p.SubjectID, now(), task.ID); err != nil {
			return Receipt{}, err
		}
		if err = recordTask(ctx, tx, p, task.ID, "task."+action, task.Revision+1, TaskChange{Paths: paths}); err != nil {
			return Receipt{}, err
		}
		if err = bumpWorkspaceClaims(ctx, tx, r.WorkspaceID); err != nil {
			return Receipt{}, err
		}
		result.Warnings, err = claimWarnings(ctx, tx, r.WorkspaceID, task.ID)
		return Receipt{ResourceID: task.ID, Revision: task.Revision + 1, Outcome: action}, err
	})
	result.Receipt = receipt
	return result, err
}

// Overlap can change in another Session of the same Workspace. Invalidate its
// view too, without changing the Task graph or any other Task's revision.
func bumpWorkspaceClaims(ctx context.Context, tx *sql.Tx, workspace string) error {
	_, err := tx.ExecContext(ctx, "UPDATE sessions SET read_revision=read_revision+1 WHERE workspace_id=?", workspace)
	return err
}

func sessionClaims(ctx context.Context, tx *sql.Tx, session string) ([]FileClaim, error) {
	rows, err := tx.QueryContext(ctx, "SELECT c.task_id,c.path,c.claimed_by,c.claimed_at FROM task_claims c JOIN tasks t ON t.id=c.task_id WHERE t.session_id=? ORDER BY c.task_id,c.path", session)
	if err != nil {
		return nil, storageError(err)
	}
	defer rows.Close()
	claims := []FileClaim{}
	for rows.Next() {
		var c FileClaim
		if err = rows.Scan(&c.TaskID, &c.Path, &c.ClaimedBy, &c.ClaimedAt); err != nil {
			return nil, storageError(err)
		}
		claims = append(claims, c)
	}
	if err = rows.Err(); err != nil {
		return nil, storageError(err)
	}
	return claims, nil
}

func claimWarnings(ctx context.Context, tx *sql.Tx, workspace, task string) ([]ClaimWarning, error) {
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.title,c.path FROM task_claims c JOIN tasks t ON t.id=c.task_id JOIN sessions s ON s.id=t.session_id
 WHERE s.workspace_id=? AND s.closed=0 AND t.progress IN ('waiting','working') AND t.id<>?
 AND EXISTS(SELECT 1 FROM task_claims own WHERE own.task_id=? AND
 (own.path=c.path OR own.path='.' OR c.path='.' OR substr(c.path,1,length(own.path)+1)=own.path||'/' OR substr(own.path,1,length(c.path)+1)=c.path||'/'))
 ORDER BY t.id,c.path`, workspace, task, task)
	if err != nil {
		return nil, storageError(err)
	}
	warnings := []ClaimWarning{}
	for rows.Next() {
		var id, title, value string
		if err = rows.Scan(&id, &title, &value); err != nil {
			rows.Close()
			return nil, storageError(err)
		}
		if len(warnings) == 0 || warnings[len(warnings)-1].TaskID != id {
			warnings = append(warnings, ClaimWarning{TaskID: id, Title: title, Paths: []string{}, Agents: []string{}})
		}
		last := &warnings[len(warnings)-1]
		last.Paths = append(last.Paths, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, storageError(err)
	}
	for i := range warnings {
		warnings[i].Agents, err = readIDs(ctx, tx, "SELECT a.name FROM task_assignees x JOIN agents a ON a.id=x.agent_id WHERE x.task_id=? ORDER BY a.name", warnings[i].TaskID)
		if err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

func (s *Store) ReadClaims(ctx context.Context, token string, r TaskReadRequest) (TaskClaims, error) {
	detail, err := s.ShowTask(ctx, token, r)
	if err != nil {
		return TaskClaims{}, err
	}
	return TaskClaims{ReadRevision: detail.ReadRevision, Claims: detail.Claims, Warnings: detail.ClaimWarnings}, nil
}
