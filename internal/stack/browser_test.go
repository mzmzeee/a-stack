package stack

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBrowserDelegationAssetsAndConditionalRead(t *testing.T) {
	f := tasksFixture(t)
	webClient := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	f.create(t, "<script>untrusted</script>")
	client := clientForCredential(t, f, f.operator)
	u, secret := viewTest(t, f, f.ref(0))
	code := u.Fragment
	u.Fragment = ""
	browser := clientForCredential(t, f, Credential{Kind: BrowserCapability, Token: secret})
	// A shell is public but contains neither data nor credentials.
	response, err := webClient.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !bytes.Contains(body, []byte("/assets/app.js")) || bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte(code)) || bytes.Contains(body, []byte("untrusted")) || !strings.Contains(response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("unsafe or unusable embedded shell")
	}
	for _, path := range []string{"/assets/app.js", "/assets/app.css"} {
		r, e := webClient.Get(f.service.Discovery.URL + path)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || len(b) == 0 || bytes.Contains(b, []byte(secret)) || bytes.Contains(b, []byte(code)) || bytes.Contains(b, []byte(f.operator.Token)) {
			t.Fatal("asset exposure")
		}
	}
	for _, change := range []string{"host", "origin", "query", "body"} {
		req, _ := http.NewRequest("GET", u.String(), nil)
		switch change {
		case "host":
			req.Host = "attacker.example"
		case "origin":
			req.Header.Set("Origin", "https://attacker.example")
		case "query":
			req.URL.RawQuery = "token=forbidden"
		case "body":
			req.Body = io.NopCloser(strings.NewReader("body"))
			req.ContentLength = 4
		}
		r, e := webClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode == 200 {
			t.Fatal("public shell bypassed guard", change)
		}
	}
	manifest, err := f.service.Store.Snapshot(ctx, secret, SnapshotRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(SnapshotRequest{Context: f.ref(0), KnownRevision: manifest.Session.ReadRevision})
	read := func(ref Context, auth bool, tag string) *http.Response {
		t.Helper()
		b := raw
		if ref != f.ref(0) {
			b, _ = json.Marshal(SnapshotRequest{Context: ref})
		}
		req, _ := http.NewRequest("POST", f.service.Discovery.URL+"/v1/session/snapshot", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-A-Stack-Instance", f.service.Discovery.InstanceID)
		if auth {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		req.Header.Set("Origin", f.service.Discovery.URL)
		req.Header.Set("If-None-Match", tag)
		r, e := webClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	first := read(f.ref(0), true, "")
	tag := first.Header.Get("ETag")
	first.Body.Close()
	if first.StatusCode != 200 || tag == "" {
		t.Fatal("missing ETag")
	}
	unchanged := read(f.ref(0), true, tag)
	b, _ := io.ReadAll(unchanged.Body)
	unchanged.Body.Close()
	if unchanged.StatusCode != 304 || len(b) != 0 {
		t.Fatal("conditional snapshot not empty 304")
	}
	denied := read(f.ref(1), true, tag)
	denied.Body.Close()
	if denied.StatusCode != 403 {
		t.Fatal("conditional read bypassed scope")
	}
	denied = read(f.ref(0), false, tag)
	denied.Body.Close()
	if denied.StatusCode != 401 {
		t.Fatal("conditional read bypassed authentication")
	}
	_, err = browser.Do(ctx, "POST", "/v1/browser/open", BrowserOpenRequest{Context: f.ref(0), OperationID: opaqueID(), CredentialHash: tokenHash(opaqueID())})
	requireCode(t, err, "forbidden")
	_, err = browser.Do(ctx, "POST", "/v1/task/create", TaskCreateRequest{Context: f.ref(0), OperationID: opaqueID(), Title: "forbidden"})
	requireCode(t, err, "forbidden")
	_, err = browser.Do(ctx, "POST", "/v1/mail/list", f.ref(0))
	requireCode(t, err, "forbidden")
	// The view link is used up, and only the Operator opens views.
	if status, _ := redeemTest(t, f, Context{f.workspace, f.sessions[0]}, code); status != 401 {
		t.Fatal("a view link worked twice", status)
	}
	for _, agent := range []Credential{f.orch, f.a} {
		for i := range 2 {
			_, err = clientForCredential(t, f, agent).BrowserURL(ctx, f.ref(i))
			requireCode(t, err, "forbidden")
		}
	}
	// Closing a Session preserves reads and permits a fresh read-only URL.
	_, err = f.service.Store.ChangeSession(ctx, f.operator.Token, "close", SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.BrowserURL(ctx, f.ref(0)); err != nil {
		t.Fatal("closed Session view", err)
	}
	// Secret never reaches a retained operation payload or receipt.
	var count int
	if err = f.service.Store.db.QueryRow("SELECT count(*) FROM operation_receipts WHERE instr(outcome,?)>0 OR instr(body_ref,?)>0 OR instr(outcome,?)>0", secret, secret, code).Scan(&count); err != nil || count != 0 {
		t.Fatal("secret retained in operation")
	}
}

func TestBrowserCapabilityReplayAndPreviewBoundary(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	q := BrowserOpenRequest{Context: f.ref(0), OperationID: opaqueID(), CredentialHash: tokenHash(opaqueID())}
	a, err := s.OpenBrowser(ctx, f.operator.Token, q)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.OpenBrowser(ctx, f.operator.Token, q)
	if err != nil || a != b {
		t.Fatal("browser delegation replay")
	}
	_, err = s.OpenBrowser(ctx, f.a.Token, q)
	requireCode(t, err, "forbidden")
	q.CredentialHash = tokenHash(opaqueID())
	_, err = s.OpenBrowser(ctx, f.operator.Token, q)
	requireCode(t, err, "operation_conflict")
	task := f.create(t, "Preview boundary")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	report := strings.Repeat("𐀀", 160) + "excluded"
	f.change(t, f.a.Token, "report", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 2, Report: &report})
	page, err := s.SnapshotPage(ctx, f.a.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks"})
	if err != nil {
		t.Fatal(err)
	}
	got := page.Items[0].(Task)
	if !utf8.ValidString(got.ReportPreview) || utf8.RuneCountInString(got.ReportPreview) != 160 || len(got.ReportPreview) != 640 || strings.Contains(got.ReportPreview, "excluded") {
		t.Fatal("preview boundary")
	}
	detail := f.show(t, task.ResourceID)
	if detail.Report != report {
		t.Fatal("preview changed full report")
	}
	if got.LastAction != "task.report" || got.QuestionPreview != "" {
		t.Fatal("last action after report", got.LastAction, got.QuestionPreview)
	}
}

func TestTaskPageQuestionPreviewAndLastAction(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	task := f.create(t, "Question preview")
	r := f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.aID})
	question := strings.Repeat("𐀀", 160) + "excluded"
	r = f.change(t, f.a.Token, "attention.set", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: r.Revision, Question: question})
	read := func() Task {
		t.Helper()
		page, err := s.SnapshotPage(ctx, f.a.Token, SnapshotPageRequest{Context: f.ref(0), Kind: "tasks"})
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if got := item.(Task); got.ID == task.ResourceID {
				return got
			}
		}
		t.Fatal("Task missing from page")
		return Task{}
	}
	got := read()
	if !got.NeedsYou || got.LastAction != "task.attention.set" || utf8.RuneCountInString(got.QuestionPreview) != 160 || strings.Contains(got.QuestionPreview, "excluded") {
		t.Fatal("question preview boundary", got.LastAction, len(got.QuestionPreview))
	}
	if f.show(t, task.ResourceID).Question.Question != question {
		t.Fatal("preview changed full question")
	}
	r = f.change(t, f.a.Token, "attention.resolve", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: r.Revision, Note: "answered"})
	if got = read(); got.NeedsYou || got.QuestionPreview != "" || got.LastAction != "task.attention.resolve" {
		t.Fatal("resolved question still previewed", got.LastAction)
	}
	f.change(t, f.a.Token, "status", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: r.Revision, Progress: "finished", Summary: "done"})
	if got = read(); got.LastAction != "task.status" || got.Progress != "finished" {
		t.Fatal("last action after status", got.LastAction)
	}
}
