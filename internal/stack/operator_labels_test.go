package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

func TestBrowserOperatorLabels(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}

	js, err := browserFiles.ReadFile("browser/app.js")
	if err != nil {
		t.Fatal(err)
	}

	label := assetBetween(t, string(js), "const operatorLabel=", "const answer=")

	script := label + `
const assert=require('node:assert/strict');
assert.equal(operatorLabel({}),'Operator');
assert.equal(operatorLabel({actor_login:'alice@example.com'}),'Operator (alice@example.com)');
assert.equal(operatorLabel({actor_login:'alice@example.com',actor_name:'Alice <script>'}),'Alice <script>');
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	for _, want := range []string{"detailText('answered',", "Answered by ${operatorLabel(a)} at", "refused?error.detail ||"} {
		if !assetContains(string(js), want) {
			t.Fatalf("missing plain-text answer display: %s", want)
		}
	}
}

func TestOperatorActorHTTPBoundary(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "serve"}[shared], func(t *testing.T) {
			f := tasksFixture(t)
			if err := f.service.SetServeHost("team.example.ts.net"); err != nil {
				t.Fatal(err)
			}

			id, q := askTest(t, f, "Proceed?")
			r := AnswerRequest{Context: f.ref(0), TaskID: id, QuestionID: q, OperationID: opaqueID(), Answer: "Proceed"}
			b, _ := json.Marshal(r)

			headers := map[string]string{"Content-Type": "application/json", "Tailscale-User-Login": "alice@example.com"}
			if shared {
				headers["Host"] = "team.example.ts.net"
			}

			status, body := rawRequest(t, f.service, f.operator, "POST", "/v1/task/answer", bytes.NewReader(b), headers)
			if status != 200 {
				t.Fatalf("status %d: %s", status, body)
			}

			a := f.show(t, id).AnsweredQuestion

			want := ""
			if shared {
				want = "alice@example.com"
			}

			if a.ActorLogin != want {
				t.Fatalf("actor %+v", a)
			}

			page, err := f.service.Store.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: id})
			if err != nil || page.Messages[0].SenderName != operatorLabel(want, "") {
				t.Fatal(page, err)
			}
		})
	}
}

func TestConcurrentOperatorAnswers(t *testing.T) {
	f := tasksFixture(t)
	id, q := askTest(t, f, "One answer?")

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		wg.Go(func() {
			c := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: login})
			_, err := f.service.Store.AnswerQuestion(c, f.operator.Token, AnswerRequest{Context: f.ref(0), TaskID: id, QuestionID: q, OperationID: opaqueID(), Answer: login})
			results <- err
		})
	}

	wg.Wait()
	close(results)

	var won, lost int
	a := f.show(t, id).AnsweredQuestion
	for err := range results {
		if err == nil {
			won++
			continue
		}

		requireCode(t, err, "conflict")

		if !strings.Contains(err.Error(), "already answered by "+operatorLabel(a.ActorLogin, a.ActorName)) {
			t.Fatal(err)
		}

		lost++
	}

	if won != 1 || lost != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_messages") != 1 {
		t.Fatal(won, lost)
	}
}

func TestOperatorLifecycleAttribution(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	c := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "alice@example.com", Name: "Alice"})

	_, err := s.ChangeOrders(c, f.operator.Token, OrdersRequest{WorkspaceID: f.workspace, OperationID: opaqueID(), ExpectedRevision: 1, Body: "Test first"})
	if err != nil {
		t.Fatal(err)
	}

	for i, action := range []string{"close", "reopen", "appoint"} {
		r := SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: int64(2 + i)}
		if action == "appoint" {
			r.AgentID = f.aID
		}

		if _, err = s.ChangeSession(c, f.operator.Token, action, r); err != nil {
			t.Fatal(action, err)
		}
	}

	_, err = s.OpenBrowser(c, f.operator.Token, BrowserOpenRequest{Context: f.ref(0), OperationID: opaqueID(), CredentialHash: tokenHash(opaqueID())})
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range []ViewRevokeRequest{{Context: f.ref(0)}, {All: true}} {
		if _, err = s.RevokeViews(c, f.operator.Token, r); err != nil {
			t.Fatal(err)
		}
	}

	for _, action := range []string{"workspace.orders.set", "session.close", "session.reopen", "session.appoint", "browser.open", "browser.revoke"} {
		var login, name string
		if err = s.db.QueryRow("SELECT actor_login,actor_name FROM lifecycle_history WHERE action=? ORDER BY sequence DESC LIMIT 1", action).Scan(&login, &name); err != nil || login != "alice@example.com" || name != "Alice" {
			t.Fatal(action, login, name, err)
		}
	}

	// Proxy identity must never relabel an Agent action.
	task, err := s.CreateTask(c, f.a.Token, TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID(), Title: "Agent action"})
	if err != nil {
		t.Fatal(err)
	}

	h := f.lastHistory(t, task.ResourceID)
	if h.ActorLogin != "" || h.ActorName != "" || h.ActorID != f.aID {
		t.Fatal(h)
	}
}

func TestSharedAnswerActor(t *testing.T) {
	f := tasksFixture(t)
	id, question := askTest(t, f, "Ship?")
	actorCtx := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "alice@example.com", Name: "Alice <script>"})
	r := AnswerRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: id, QuestionID: question, Answer: "Yes"}

	receipt, err := f.service.Store.AnswerQuestion(actorCtx, f.operator.Token, r)
	if err != nil {
		t.Fatal(err)
	}

	for _, value := range []any{f.show(t, id).AnsweredQuestion, f.lastHistory(t, id)} {
		b, _ := json.Marshal(value)
		if !strings.Contains(string(b), `"actor_login":"alice@example.com"`) {
			t.Fatalf("missing attribution: %s", b)
		}
	}

	page, err := f.service.Store.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: id})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].SenderName != "Alice <script>" {
		t.Fatal(page, err)
	}

	r.OperationID = opaqueID()
	_, err = f.service.Store.AnswerQuestion(ctx, f.operator.Token, r)
	requireCode(t, err, "conflict")

	if !strings.Contains(err.Error(), "already answered by Alice <script>") {
		t.Fatal(err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_messages") != 1 {
		t.Fatal("duplicate answer")
	}

	if receipt.ReceivedAt == "" || receipt.ActorLogin != "alice@example.com" || receipt.ActorName != "Alice <script>" {
		t.Fatal("missing answer receipt attribution", receipt)
	}

	r.OperationID = receipt.OperationID
	again, err := f.service.Store.AnswerQuestion(ctx, f.operator.Token, r)
	if err != nil || again != receipt {
		t.Fatal("replay must retain original actor", again, err)
	}

	f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: id, ExpectedRevision: receipt.Revision, Question: "And the next release?"})
	r.OperationID = opaqueID()
	_, err = f.service.Store.AnswerQuestion(ctx, f.operator.Token, r)
	requireCode(t, err, "conflict")

	if !strings.Contains(err.Error(), "already answered by Alice <script>") || f.show(t, id).Question == nil {
		t.Fatal("old answer must not replace the new question", err)
	}
}
