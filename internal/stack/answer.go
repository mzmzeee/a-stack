package stack

import (
	"context"
	"database/sql"
	"net/http"
)

// MaxAnswerBytes bounds the Operator's answer to a Needs you question.
const MaxAnswerBytes = 4 << 10

// AnswerRequest answers the open Needs you question of a Task. QuestionID is
// the guard: it names the question that the Operator read. A question never
// changes, and a new one gets a new ID.
type AnswerRequest struct {
	Context
	OperationID string `json:"operation_id"`
	TaskID      string `json:"task_id"`
	QuestionID  string `json:"question_id"`
	Answer      string `json:"answer"`
}

// AnswerQuestion is the Operator's answer, from the CLI or from the browser
// view of the Task's Session, the view's one write. In one operation it
// stores the answer as the question's note and resolves the question with no
// Agent resolver, and sends the answer as Task mail from the Operator that
// mentions every current assignee, so each gets the normal notice.
func (s *Store) AnswerQuestion(ctx context.Context, token string, r AnswerRequest) (Receipt, error) {
	return s.Mutate(ctx, token, operation(r.OperationID, "task.answer", r.SessionID, r), replayTask(false), func(ctx context.Context, tx *sql.Tx, p Principal) (Receipt, error) {
		if p.Kind != OperatorCapability && p.Kind != BrowserCapability {
			return Receipt{}, failure("forbidden", "only the Operator answers a Needs you question")
		}
		if _, err := OpenSession(ctx, tx, p, r.Context); err != nil {
			return Receipt{}, err
		}
		if err := validPlainText("answer", r.Answer, MaxAnswerBytes); err != nil {
			return Receipt{}, err
		}
		if !validToken(r.QuestionID) {
			return Receipt{}, failure("invalid_input", "question ID required; task show names the open question")
		}
		task, err := taskInSession(ctx, tx, r.TaskID, r.SessionID)
		if err != nil {
			return Receipt{}, err
		}
		answered, err := scanQuestion(tx.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM task_questions WHERE id=? AND task_id=?", r.QuestionID, task.ID))
		if err != nil {
			return Receipt{}, err
		}
		if answered != nil && answered.Answer != "" {
			return Receipt{}, &Error{Code: "conflict", Message: "Needs you question already answered by " + operatorLabel(answered.ActorLogin, answered.ActorName) + " at " + answered.ResolvedAt, CurrentRevision: &task.Revision, CurrentProgress: task.Progress}
		}
		question, err := openQuestion(ctx, tx, task.ID)
		if err != nil {
			return Receipt{}, err
		}
		if question == nil {
			return Receipt{}, &Error{Code: "conflict", Message: "Task has no open Needs you question; it was answered or resolved", CurrentRevision: &task.Revision, CurrentProgress: task.Progress}
		}
		if question.ID != r.QuestionID {
			return Receipt{}, &Error{Code: "conflict", Message: "the Needs you question changed; read it again before you answer", CurrentRevision: &task.Revision, CurrentProgress: task.Progress, CurrentQuestion: question}
		}
		timestamp := now()
		login, name := operatorActor(ctx, p)
		if _, err = tx.ExecContext(ctx, "UPDATE task_questions SET note=?,resolved_at=?,actor_login=?,actor_name=? WHERE id=?", r.Answer, timestamp, login, name, question.ID); err != nil {
			return Receipt{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE tasks SET revision=revision+1,updated_at=? WHERE id=?", timestamp, task.ID); err != nil {
			return Receipt{}, err
		}
		if err = recordTask(ctx, tx, p, task.ID, "task.answer", task.Revision+1, TaskChange{QuestionID: question.ID}); err != nil {
			return Receipt{}, err
		}
		var sequence int64
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM mail_messages WHERE task_id=?", task.ID).Scan(&sequence); err != nil {
			return Receipt{}, err
		}
		id := opaqueID()
		if _, err = tx.ExecContext(ctx, "INSERT INTO mail_messages(id,task_id,sequence,sender_kind,sender_id,received_at,body,actor_login,actor_name,priority) VALUES(?,?,?,'operator',NULL,?,?,?,?,'urgent')", id, task.ID, sequence, timestamp, r.Answer, login, name); err != nil {
			return Receipt{}, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO mail_mentions SELECT ?,task_id,agent_id,?,? FROM task_assignees WHERE task_id=?", id, NoticePending, timestamp, task.ID); err != nil {
			return Receipt{}, err
		}
		if err = bumpSessionRead(ctx, tx, r.SessionID); err != nil {
			return Receipt{}, err
		}
		return Receipt{ResourceID: task.ID, BodyRef: id, Revision: task.Revision + 1, Outcome: "answered"}, nil
	})
}

// answerHTTP serves POST /v1/task/answer. dispatch lets a view token reach it
// only with the Origin of the service.
func (s *Service) answerHTTP(w http.ResponseWriter, r *http.Request, token string) {
	if r.Method != http.MethodPost {
		writeError(w, failure("invalid_input", "POST JSON required"))
		return
	}
	var q AnswerRequest
	err := requestJSON(w, r, &q)
	var receipt Receipt
	if err == nil {
		receipt, err = s.Store.AnswerQuestion(r.Context(), token, q)
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": receipt})
}
