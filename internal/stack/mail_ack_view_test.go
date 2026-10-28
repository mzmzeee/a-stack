package stack

import (
	"os/exec"
	"regexp"
	"testing"
	"time"
)

func TestMailAckDeadlineChangesETagWithoutWrite(t *testing.T) {
	at := time.Now().UTC()
	fixedClock.Store(&at)
	t.Cleanup(func() { fixedClock.Store(nil) })
	f := mailFixture(t)
	ackSend(t, f, []string{"03-worker"}, "1h")
	_, token := viewTest(t, f.taskTeam, f.ref(0))
	poll := viewPoller(t, f.taskTeam, token)
	_, tag, first := poll(0, "")
	next := at.Add(30 * time.Minute)
	fixedClock.Store(&next)
	if status, _, _ := poll(first.Session.ReadRevision, tag); status != 304 {
		t.Fatal(status)
	}
	next = at.Add(2 * time.Hour)
	fixedClock.Store(&next)
	status, newTag, snapshot := poll(first.Session.ReadRevision, tag)
	if status != 200 || newTag == tag || snapshot.Unchanged || snapshot.Session.ReadRevision != first.Session.ReadRevision {
		t.Fatal(status, newTag, snapshot)
	}
	if status, _, _ := poll(first.Session.ReadRevision, newTag); status != 304 {
		t.Fatal(status)
	}
}

func TestMailAckBrowserReadOnly(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	r := ackSend(t, f, []string{"03-worker"}, "1h")
	browser := issueTest(t, s, BrowserCapability, "ack-view", f.sessions[0])
	client := clientForCredential(t, f.taskTeam, browser)
	_, err := client.Do(ctx, "POST", "/v1/mail/ack", MailAckRequest{Context: f.ref(0), OperationID: opaqueID(), MessageID: r.ResourceID})
	requireCode(t, err, "forbidden")
	page, err := s.ReadMail(ctx, browser.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(page.PendingAcks) != 1 || page.PendingAcks[0].AckedAt != "" {
		t.Fatal(page, err)
	}
	if len(ackCheck(t, f, f.b.Token).PendingAcks) != 1 {
		t.Fatal("browser read acknowledged")
	}
}

func TestBrowserAckPlainText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	js, err := browserFiles.ReadFile("browser/app.js")
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^  const report=firstLine\(t.report_preview\).*;$`).Find(js)
	if len(line) == 0 {
		t.Fatal("missing Now acknowledgement text")
	}
	script := `const assert=require('node:assert/strict');
const firstLine=text=>(text||'').split('\n')[0], plural=(n,s)=>n+' '+s+(n===1?'':'s');
const setText=(node,text)=>{node.textContent=text};
const c={note:{}};
for (const t of [{overdue_acks:2,report_preview:'<img src=x onerror=alert(1)>'},{overdue_acks:0,report_preview:''},{overdue_acks:1,report_preview:'Done'}]) {
` + string(line) + `
assert.equal(c.note.textContent,(t.overdue_acks?plural(t.overdue_acks,'overdue acknowledgement')+'. ':'')+(t.report_preview||'No report yet.'));
assert.equal(c.note.innerHTML,undefined);
}
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
