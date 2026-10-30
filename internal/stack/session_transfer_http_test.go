package stack

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func transferCall[T any](t *testing.T, c *Client, path string, request any) T {
	t.Helper()
	data, err := c.Do(ctx, "POST", path, request)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var envelope struct {
		Data T `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = decoder.Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestTransferThroughTLSAndFriendLink(t *testing.T) {
	source := tasksFixture(t)
	server := tasksFixture(t)
	server.service.Version = "test"
	task, question := askTest(t, source, "Share one answer?")
	local, err := NewClient(source.dir, "", OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}
	const host = "server.example.ts.net"
	if err = server.service.SetServeHost(host); err != nil {
		t.Fatal(err)
	}
	backend, _ := url.Parse(server.service.Discovery.URL)
	var login atomic.Value
	login.Store("owner@example.com")
	proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(backend)
		r.Out.Host = host
		r.Out.Header.Del("Tailscale-User-Login")
		r.Out.Header.Del("Tailscale-User-Name")
		r.Out.Header.Set("Tailscale-User-Login", login.Load().(string))
	}})
	defer proxy.Close()
	destination := &Client{Discovery: server.service.Discovery, Credential: server.operator, HTTP: proxy.Client()}
	destination.Discovery.URL = proxy.URL
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("AppData", config)
	if _, err = connectRemote(ctx, "team", proxy.URL, proxy.Client()); err != nil {
		t.Fatal(err)
	}
	ownerCode := opaqueID()
	transferCall[any](t, destination, "/v1/connect/code", map[string]string{"code_hash": tokenHash(ownerCode)})
	ownerFile, err := RemoteOwnerCredential("team")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = joinRemoteCredentialUsing(ctx, "team", "", ownerFile, ownerCode, proxy.Client()); err != nil {
			t.Fatal(err)
		}
	}
	// The default remote credential is the enrolled server owner, never the local owner.
	destination, err = newRemoteClient(ctx, "team", "", OperatorCapability, proxy.Client())
	if err != nil {
		t.Fatal(err)
	}
	transferID := opaqueID()
	bundle := transferCall[TransferBundle](t, local, "/v1/session/transfer/export", TransferExportRequest{Context: source.ref(0), TransferID: transferID, Repository: "example.com/team/repo"})
	secrets := map[string]string{source.orchID: opaqueID(), source.aID: opaqueID(), source.bID: opaqueID()}
	hashes := map[string]string{}
	for id, secret := range secrets {
		hashes[id] = tokenHash(secret)
	}
	imported := transferCall[TransferStatus](t, destination, "/v1/session/transfer/import", TransferImportRequest{Bundle: bundle, AgentHashes: hashes, Machine: "laptop"})
	if imported.State != "staged" {
		t.Fatal(imported)
	}
	transferCall[TransferStatus](t, local, "/v1/session/transfer/seal", TransferChangeRequest{Context: source.ref(0), TransferID: transferID, DestinationDatabase: server.service.Store.id, Remote: "team"})
	active := transferCall[TransferStatus](t, destination, "/v1/session/transfer/activate", TransferChangeRequest{Context: imported.Context, TransferID: transferID})
	// Repeating activation models a lost acknowledgement, not a new import.
	transferCall[TransferStatus](t, destination, "/v1/session/transfer/activate", TransferChangeRequest{Context: active.Context, TransferID: transferID})
	if _, err = local.Do(ctx, "POST", "/v1/session/inspect", source.ref(0)); err == nil {
		t.Fatal("sealed source remained readable")
	}
	worker := *destination
	worker.Credential = Credential{Kind: AgentCapability, Token: secrets[source.aID]}
	view := transferCall[Session](t, &worker, "/v1/session/inspect", active.Context)
	if view.YourAgentID != source.aID {
		t.Fatal("Agent identity changed")
	}
	transferCall[Receipt](t, destination, "/v1/session/invite", SessionInviteRequest{CodeHash: tokenHash("test-join-code"), Context: active.Context, OperationID: opaqueID(), Login: "friend@example.com"})
	friendSecret := opaqueID()
	unauthenticated := *destination
	unauthenticated.Credential = Credential{}
	login.Store("stranger@example.com")
	_, err = unauthenticated.Do(ctx, http.MethodPost, "/v1/session/join", SessionJoinRequest{Code: "test-join-code", SessionID: active.SessionID, CredentialHash: tokenHash(friendSecret)})
	requireCode(t, err, "forbidden")
	login.Store("friend@example.com")
	ref := transferCall[Context](t, &unauthenticated, "/v1/session/join", SessionJoinRequest{Code: "test-join-code", SessionID: active.SessionID, CredentialHash: tokenHash(friendSecret)})
	friend := *destination
	friend.Credential = Credential{Kind: OperatorCapability, Token: friendSecret}
	_, err = friend.Do(ctx, "POST", "/v1/session/inspect", server.ref(0))
	requireCode(t, err, "forbidden")
	transferCall[Receipt](t, &friend, "/v1/task/answer", AnswerRequest{Context: ref, TaskID: task, QuestionID: question, OperationID: opaqueID(), Answer: "Yes, together"})
	friendLocal, friendDir, _ := startTest(t)
	_ = friendLocal
	friendClient, err := NewClient(friendDir, "", OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}
	link := SessionLink{Context: ref, Path: privateTestDir(t), Remote: "team", DatabaseID: server.service.Store.id, CredentialFile: filepath.Join(privateTestDir(t), "friend.json"), Name: bundle.Session.Name}
	transferCall[SessionLink](t, friendClient, "/v1/session/link", SessionLinkRequest{Link: link})
	links := transferCall[[]SessionLink](t, friendClient, "/v1/session/links", struct{}{})
	if len(links) != 1 || links[0].Context != ref {
		t.Fatal("friend linked a different Session")
	}
	reverse := transferCall[TransferBundle](t, &friend, "/v1/session/transfer/export", TransferExportRequest{Context: ref, TransferID: opaqueID(), Repository: bundle.Repository})
	for id := range hashes {
		hashes[id] = tokenHash(opaqueID())
	}
	restored := transferCall[TransferStatus](t, local, "/v1/session/transfer/import", TransferImportRequest{Bundle: reverse, AgentHashes: hashes, Machine: "laptop", LocalRuntime: true})
	transferCall[TransferStatus](t, &friend, "/v1/session/transfer/seal", TransferChangeRequest{Context: ref, TransferID: reverse.TransferID, DestinationDatabase: source.service.Store.id})
	transferCall[TransferStatus](t, local, "/v1/session/transfer/activate", TransferChangeRequest{Context: restored.Context, TransferID: reverse.TransferID})
	got, err := source.service.Store.ShowTask(ctx, source.operator.Token, TaskReadRequest{Context: restored.Context, TaskID: task})
	if err != nil || got.AnsweredQuestion == nil || got.AnsweredQuestion.ActorLogin != "friend@example.com" {
		t.Fatal("answer/attribution did not return", err)
	}
	_, err = friend.Do(ctx, "POST", "/v1/session/inspect", ref)
	requireCode(t, err, "conflict")
}
