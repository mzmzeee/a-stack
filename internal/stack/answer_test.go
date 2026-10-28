package stack

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// askTest assigns 02-worker and 03-worker to a new Task, lets 02-worker ask a
// question, and returns the Task ID and the question ID.
func askTest(t *testing.T, f taskTeam, question string) (string, string) {
	t.Helper()
	id := f.create(t, "Login").ResourceID
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: 1, AgentID: f.aID})
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: id, ExpectedRevision: 2, AgentID: f.bID})
	f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: id, ExpectedRevision: 3, Question: question})
	return id, f.show(t, id).Question.ID
}

// answerRaw posts an answer like the view: JSON, the instance header and the
// given Origin, which is left out when empty.
func answerRaw(t *testing.T, f taskTeam, token, origin string, r AnswerRequest, headers map[string]string) (int, *Error) {
	t.Helper()
	body, _ := json.Marshal(r)
	all := map[string]string{"Content-Type": "application/json"}
	if origin != "" {
		all["Origin"] = origin
	}
	for k, v := range headers {
		all[k] = v
	}
	status, data := rawRequest(t, f.service, Credential{Kind: BrowserCapability, Token: token}, "POST", "/v1/task/answer", bytes.NewReader(body), all)
	var envelope struct {
		Error *Error `json:"error"`
	}
	_ = json.Unmarshal(data, &envelope)
	return status, envelope.Error
}

func TestOperatorAnswerResolvesQuestionAndMailsTheAssignees(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	id, question := askTest(t, f, "GitHub or Google first?")
	request := AnswerRequest{Context: f.ref(0), OperationID: "operator-answer-1", TaskID: id, QuestionID: question, Answer: "Google first, then GitHub."}
	receipt, err := s.AnswerQuestion(ctx, f.operator.Token, request)
	if err != nil || receipt.Outcome != "answered" || receipt.Revision != 5 || receipt.ResourceID != id || receipt.BodyRef == "" {
		t.Fatal(receipt, err)
	}
	// A retry with the same operation ID returns the same receipt and sends
	// no second mail.
	again, err := s.AnswerQuestion(ctx, f.operator.Token, request)
	if err != nil || again != receipt {
		t.Fatal("replay differs", again, err)
	}
	task := f.show(t, id)
	if task.NeedsYou || task.Question != nil || task.Revision != 5 || task.LastAction != "task.answer" || task.MailCount != 1 {
		t.Fatalf("task after answer: %+v", task)
	}
	if a := task.AnsweredQuestion; a == nil || a.ID != question || a.Question != "GitHub or Google first?" || a.Answer != request.Answer || a.Note != "" || a.ResolverID != "" || a.ResolvedAt == "" {
		t.Fatalf("answered question: %+v", a)
	}
	last := f.lastHistory(t, id)
	if last.Action != "task.answer" || last.ActorKind != "operator" || last.ActorID != "" || last.Revision != 5 || last.Question == nil || last.Question.Answer != request.Answer {
		t.Fatalf("history: %+v", last)
	}
	// Both assignees read the answer as Task mail from the Operator, with a
	// pending notice each.
	for _, c := range []Credential{f.a, f.b} {
		page, err := s.CheckMail(ctx, c.Token, MailCheckRequest{Context: f.ref(0)})
		if err != nil || len(page.Tasks) != 1 || len(page.Tasks[0].Messages) != 1 {
			t.Fatal(page, err)
		}
		m := page.Tasks[0].Messages[0]
		if m.SenderKind != "operator" || m.SenderName != "Operator" || m.SenderID != "" || m.Body != request.Answer || len(m.MentionIDs) != 2 {
			t.Fatalf("mail: %+v", m)
		}
	}
	if n := countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'"); n != 2 {
		t.Fatal("pending notices", n)
	}
	// An Agent's own resolution keeps its note and resolver.
	f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: id, ExpectedRevision: 5, Question: "Which scopes?"})
	f.change(t, f.a.Token, "attention.resolve", TaskChangeRequest{TaskID: id, ExpectedRevision: 6, Note: "Found them in the ticket"})
	if a := f.show(t, id).AnsweredQuestion; a == nil || a.ID != question {
		t.Fatal("answered_question must stay the Operator's answer", a)
	}
	if q := f.lastHistory(t, id).Question; q.Note != "Found them in the ticket" || q.Answer != "" || q.ResolverID != f.aID {
		t.Fatalf("Agent resolution: %+v", q)
	}
}

func TestAnswerRefusals(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	id, question := askTest(t, f, "Ship today?")
	answer := func(token string, r AnswerRequest) error {
		if r.Context == (Context{}) {
			r.Context = f.ref(0)
		}
		if r.TaskID == "" {
			r.TaskID = id
		}
		if r.QuestionID == "" {
			r.QuestionID = question
		}
		if r.Answer == "" {
			r.Answer = "Yes"
		}
		r.OperationID = opaqueID()
		_, err := s.AnswerQuestion(ctx, token, r)
		return err
	}
	// Only the Operator answers: not an assignee, not the Orchestrator.
	requireCode(t, answer(f.a.Token, AnswerRequest{}), "forbidden")
	requireCode(t, answer(f.orch.Token, AnswerRequest{}), "forbidden")
	// Bounded plain text.
	requireCode(t, answer(f.operator.Token, AnswerRequest{Answer: strings.Repeat("x", MaxAnswerBytes+1)}), "invalid_input")
	requireCode(t, answer(f.operator.Token, AnswerRequest{Answer: " \n"}), "invalid_input")
	requireCode(t, answer(f.operator.Token, AnswerRequest{Answer: "bad \xff"}), "invalid_input")
	// Plain text, as for standing orders: no control characters but tab and
	// line breaks.
	for _, text := range []string{"A\x1b[31mRED", "title\x1b]0;x\a", "bell\a", "del\x7f", "nul\x00"} {
		err := answer(f.operator.Token, AnswerRequest{Answer: text})
		requireCode(t, err, "invalid_input")
		if !strings.Contains(err.Error(), "answer must be plain text without control characters") {
			t.Fatalf("%q: %v", text, err)
		}
	}
	requireCode(t, answer(f.operator.Token, AnswerRequest{QuestionID: "-"}), "invalid_input")
	// A Task of the other Session is not found in this one.
	requireCode(t, answer(f.operator.Token, AnswerRequest{Context: f.ref(1)}), "not_found")
	// A stale question: 02-worker resolved it and asked a new one.
	f.change(t, f.a.Token, "attention.resolve", TaskChangeRequest{TaskID: id, ExpectedRevision: 4, Note: "Asked in chat"})
	requireCode(t, answer(f.operator.Token, AnswerRequest{}), "conflict")
	f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: id, ExpectedRevision: 5, Question: "Ship tomorrow?"})
	err := answer(f.operator.Token, AnswerRequest{})
	requireCode(t, err, "conflict")
	if e := err.(*Error); e.CurrentQuestion == nil || e.CurrentQuestion.Question != "Ship tomorrow?" || *e.CurrentRevision != 6 {
		t.Fatalf("stale answer error: %+v", e)
	}
	if n := countTest(t, s, "SELECT count(*) FROM mail_messages"); n != 0 {
		t.Fatal("a refused answer sent mail", n)
	}
	if _, err = s.AnswerQuestion(ctx, f.operator.Token, AnswerRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, QuestionID: f.show(t, id).Question.ID, Answer: "Yes,\ttomorrow.\r\nAt nine."}); err != nil {
		t.Fatal("tab and line breaks:", err)
	}
	f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: id, ExpectedRevision: 7, Question: "And the docs?"})
	// A closed Session takes no answer.
	if _, err = s.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2}); err != nil {
		t.Fatal(err)
	}
	requireCode(t, answer(f.operator.Token, AnswerRequest{QuestionID: f.show(t, id).Question.ID}), "session_closed")
}

func TestViewAnswersOnlyItsSessionWithItsOrigin(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	id, question := askTest(t, f, "Merge now?")
	_, token := viewTest(t, f, f.ref(0))
	_, other := viewTest(t, f, f.ref(1))
	origin := f.service.Discovery.URL
	request := func() AnswerRequest {
		return AnswerRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, QuestionID: question, Answer: "Yes, merge."}
	}
	refused := []struct {
		name    string
		token   string
		origin  string
		r       AnswerRequest
		headers map[string]string
		status  int
		code    string
	}{
		{"no Origin", token, "", request(), nil, 403, "forbidden"},
		{"bad Origin", token, "http://evil.example", request(), nil, 403, "forbidden"},
		{"bad Host", token, origin, request(), map[string]string{"Host": "localhost"}, 403, "forbidden"},
		{"other instance", token, origin, request(), map[string]string{"X-A-Stack-Instance": opaqueID()}, 409, "conflict"},
		{"form post", token, origin, request(), map[string]string{"Content-Type": "text/plain"}, 400, "invalid_input"},
		{"no token", "", origin, request(), nil, 401, "unauthorized"},
		{"other Session's view", other, origin, request(), nil, 403, "forbidden"},
		{"Task of another Session", other, origin, AnswerRequest{Context: f.ref(1), OperationID: opaqueID(), TaskID: id, QuestionID: question, Answer: "Yes"}, nil, 404, "not_found"},
		{"oversized", token, origin, AnswerRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, QuestionID: question, Answer: strings.Repeat("é", MaxAnswerBytes/2+1)}, nil, 400, "invalid_input"},
		{"stale question", token, origin, AnswerRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, QuestionID: opaqueID(), Answer: "Yes"}, nil, 409, "conflict"},
	}
	for _, c := range refused {
		status, e := answerRaw(t, f, c.token, c.origin, c.r, c.headers)
		if status != c.status || e == nil || e.Code != c.code {
			t.Fatalf("%s: %d %+v", c.name, status, e)
		}
	}
	if task := f.show(t, id); !task.NeedsYou || task.MailCount != 0 {
		t.Fatal("a refused answer changed the Task")
	}
	// The view token still writes nothing else.
	status, _ := rawRequest(t, f.service, Credential{Token: token}, "POST", "/v1/mail/send", strings.NewReader(`{}`), map[string]string{"Content-Type": "application/json", "Origin": origin})
	if status != 403 {
		t.Fatal("view token sent mail", status)
	}
	_, err := s.ChangeTask(ctx, token, "attention.resolve", TaskChangeRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, ExpectedRevision: 4, Note: "x"})
	requireCode(t, err, "forbidden")
	// The answer works once; then the Task has no open question.
	if status, e := answerRaw(t, f, token, origin, request(), nil); status != 200 || e != nil {
		t.Fatal("view answer", status, e)
	}
	if last := f.lastHistory(t, id); last.Action != "task.answer" || last.ActorKind != "operator" {
		t.Fatalf("history: %+v", last)
	}
	status, e := answerRaw(t, f, token, origin, request(), nil)
	if status != 409 || e == nil || e.Code != "conflict" || !strings.Contains(e.Message, "already answered by Operator at ") {
		t.Fatal("second answer", status, e)
	}
	// The Operator CLI path needs no Origin.
	if _, err = clientForCredential(t, f, f.operator).Do(ctx, "POST", "/v1/task/answer", request()); err == nil {
		t.Fatal("answered a resolved question")
	}
	requireCode(t, err, "conflict")
}
