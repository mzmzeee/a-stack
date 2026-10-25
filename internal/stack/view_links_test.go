package stack

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// Old stores in upgrade tests run today's authentication, which reads
// capabilities.expires_at. They add the column with expiresStandIn and drop it
// again before the upgrade.
const expiresStandIn = "ALTER TABLE capabilities ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;"
const dropExpires = "ALTER TABLE capabilities DROP COLUMN expires_at;"

// undoMigration7 turns a current database back into the v6 shape.
const undoMigration7 = "DROP TABLE view_links;" + dropExpires

// redeemTest sends a view link's code to the redeem endpoint, like app.js.
func redeemTest(t *testing.T, f taskTeam, ref Context, code string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(ViewRedeemRequest{Context: ref, Code: code})
	req, _ := http.NewRequest("POST", f.service.Discovery.URL+"/v1/browser/redeem", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", f.service.Discovery.URL)
	req.Header.Set("X-A-Stack-Instance", f.service.Discovery.InstanceID)
	response, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var envelope struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.NewDecoder(response.Body).Decode(&envelope)
	return response.StatusCode, envelope.Data.Token
}

// viewTest opens a view link as the Operator and redeems it. It returns the
// link and the view token.
func viewTest(t *testing.T, f taskTeam, ref Context) (*url.URL, string) {
	t.Helper()
	link, err := clientForCredential(t, f, f.operator).BrowserURL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(link)
	if err != nil || !validToken(u.Fragment) || u.RawQuery != "" || u.Path != "/view/"+ref.WorkspaceID+"/"+ref.SessionID {
		t.Fatal("invalid view URL")
	}
	status, token := redeemTest(t, f, ref, u.Fragment)
	if status != 200 || !validToken(token) || token == u.Fragment {
		t.Fatal("view link not redeemed", status)
	}
	return u, token
}

func codeOf(t *testing.T, f taskTeam, ref Context) string {
	t.Helper()
	link, err := clientForCredential(t, f, f.operator).BrowserURL(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(link)
	return u.Fragment
}

// The review reproduction: an Agent that is not assigned to a Task made a
// browser credential and read the Task's mail through it.
func TestAgentCannotOpenAViewToReadOtherTasksMail(t *testing.T) {
	f := tasksFixture(t)
	task := f.create(t, "Not for 02-worker")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: 1, AgentID: f.bID})
	if _, err := f.service.Store.SendMail(ctx, f.b.Token, MailSendRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: task.ResourceID, Body: "private"}); err != nil {
		t.Fatal(err)
	}
	_, err := f.service.Store.ReadMail(ctx, f.a.Token, MailReadRequest{Context: f.ref(0), TaskID: task.ResourceID})
	requireCode(t, err, "forbidden")
	agent := clientForCredential(t, f, f.a)
	_, err = agent.BrowserURL(ctx, f.ref(0))
	requireCode(t, err, "forbidden")
	_, err = agent.Do(ctx, "POST", "/v1/browser/open", BrowserOpenRequest{Context: f.ref(0), OperationID: opaqueID(), CredentialHash: tokenHash(opaqueID())})
	requireCode(t, err, "forbidden")
	if countTest(t, f.service.Store, "SELECT count(*) FROM view_links")+countTest(t, f.service.Store, "SELECT count(*) FROM capabilities WHERE kind='browser'") != 0 {
		t.Fatal("an Agent made a view credential")
	}
}

func TestViewLinkWorksOnceAndExpires(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	start := time.Now()
	at := func(d time.Duration) {
		moment := start.Add(d)
		fixedClock.Store(&moment)
	}
	at(0)
	t.Cleanup(func() { fixedClock.Store(nil) })
	_, token := viewTest(t, f, f.ref(0))
	// A code for another Session fails and is used up all the same.
	code := codeOf(t, f, f.ref(0))
	if status, _ := redeemTest(t, f, f.ref(1), code); status != 401 {
		t.Fatal("a code worked for another Session", status)
	}
	if status, _ := redeemTest(t, f, f.ref(0), code); status != 401 {
		t.Fatal("a code worked after a failed redeem", status)
	}
	if status, _ := redeemTest(t, f, f.ref(0), opaqueID()); status != 401 {
		t.Fatal("an unknown code worked", status)
	}
	// The code works until ViewLinkAge has passed.
	early, late := codeOf(t, f, f.ref(0)), codeOf(t, f, f.ref(0))
	at(ViewLinkAge - time.Second)
	if status, _ := redeemTest(t, f, f.ref(0), early); status != 200 {
		t.Fatal("a fresh code failed", status)
	}
	at(ViewLinkAge)
	if status, _ := redeemTest(t, f, f.ref(0), late); status != 401 {
		t.Fatal("an expired code worked", status)
	}
	// The code is never a bearer credential.
	bearer := codeOf(t, f, f.ref(0))
	_, err := s.Snapshot(ctx, bearer, SnapshotRequest{Context: f.ref(0)})
	requireCode(t, err, "unauthorized")
	// The view token works until ViewTokenAge has passed.
	at(ViewTokenAge - time.Second)
	if _, err = s.Snapshot(ctx, token, SnapshotRequest{Context: f.ref(0)}); err != nil {
		t.Fatal("view token expired early", err)
	}
	at(ViewTokenAge)
	_, err = s.Snapshot(ctx, token, SnapshotRequest{Context: f.ref(0)})
	requireCode(t, err, "unauthorized")
	// The next link deletes the expired links and view tokens.
	codeOf(t, f, f.ref(0))
	if countTest(t, s, "SELECT count(*) FROM view_links") != 1 || countTest(t, s, "SELECT count(*) FROM capabilities WHERE kind='browser'") != 1 {
		t.Fatal("expired view credentials were kept")
	}
	if countTest(t, s, "SELECT count(*) FROM capabilities WHERE kind='agent'") != 3 {
		t.Fatal("cleanup touched Agent credentials")
	}
}

func TestRevokeViews(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	_, first := viewTest(t, f, f.ref(0))
	_, second := viewTest(t, f, f.ref(0))
	_, other := viewTest(t, f, f.ref(1))
	pending := codeOf(t, f, f.ref(0))
	operator := clientForCredential(t, f, f.operator)
	for _, bad := range []ViewRevokeRequest{{}, {Context: f.ref(0), All: true}, {Context: Context{f.workspace, ""}}} {
		_, err := operator.RevokeViews(ctx, bad)
		requireCode(t, err, "invalid_input")
	}
	for _, agent := range []Credential{f.orch, f.a} {
		_, err := clientForCredential(t, f, agent).RevokeViews(ctx, ViewRevokeRequest{Context: f.ref(0)})
		requireCode(t, err, "forbidden")
	}
	_, err := clientForCredential(t, f, Credential{Kind: BrowserCapability, Token: first}).RevokeViews(ctx, ViewRevokeRequest{Context: f.ref(0)})
	requireCode(t, err, "forbidden")
	revoked := func(r ViewRevokeRequest) int {
		t.Helper()
		data, err := operator.RevokeViews(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Data struct {
				Revoked int `json:"revoked"`
			} `json:"data"`
		}
		if json.Unmarshal(data, &envelope) != nil {
			t.Fatal("revoke JSON")
		}
		return envelope.Data.Revoked
	}
	if n := revoked(ViewRevokeRequest{Context: f.ref(0)}); n != 2 {
		t.Fatal("revoked", n)
	}
	for _, token := range []string{first, second} {
		_, err = s.Snapshot(ctx, token, SnapshotRequest{Context: f.ref(0)})
		requireCode(t, err, "unauthorized")
	}
	if status, _ := redeemTest(t, f, f.ref(0), pending); status != 401 {
		t.Fatal("a revoked link still worked", status)
	}
	if _, err = s.Snapshot(ctx, other, SnapshotRequest{Context: f.ref(1)}); err != nil {
		t.Fatal("revoke reached another Session", err)
	}
	if n := revoked(ViewRevokeRequest{Context: f.ref(0)}); n != 0 {
		t.Fatal("second revoke", n)
	}
	if n := revoked(ViewRevokeRequest{All: true}); n != 1 {
		t.Fatal("revoke all", n)
	}
	_, err = s.Snapshot(ctx, other, SnapshotRequest{Context: f.ref(1)})
	requireCode(t, err, "unauthorized")
	if _, err = s.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)}); err != nil {
		t.Fatal("revoke touched an Agent credential", err)
	}
}

func TestMigration7DeletesOldBrowserCredentials(t *testing.T) {
	f := tasksFixture(t)
	_, token := viewTest(t, f, f.ref(0))
	path, state := f.service.Store.path, f.service.lock.dir
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	// A v6 database holds browser credentials that never expire.
	legacy := opaqueID()
	if _, err = db.Exec(undoMigration9+undoMigration7+"INSERT INTO capabilities VALUES(?,'browser','legacy-view',?);PRAGMA user_version=6", tokenHash(legacy), f.sessions[1]); err != nil {
		t.Fatal(err)
	}
	db.Close()
	service := serveTest(t, state, "127.0.0.1:0")
	f.service = service
	s := service.Store
	if countTest(t, s, "PRAGMA user_version") != schemaVersion || countTest(t, s, "SELECT count(*) FROM capabilities WHERE kind='browser'") != 0 || countTest(t, s, "SELECT count(*) FROM view_links") != 0 {
		t.Fatal("migration 7 kept browser credentials")
	}
	for _, old := range []string{token, legacy} {
		_, err := s.Snapshot(ctx, old, SnapshotRequest{Context: f.ref(0)})
		requireCode(t, err, "unauthorized")
	}
	if _, err := s.Snapshot(ctx, f.a.Token, SnapshotRequest{Context: f.ref(0)}); err != nil {
		t.Fatal("migration 7 lost an Agent credential", err)
	}
	viewTest(t, f, f.ref(0))
}
