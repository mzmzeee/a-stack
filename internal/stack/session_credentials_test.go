package stack

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type lostJoinResponse struct {
	base                http.RoundTripper
	drop, blockRecovery bool
}

func (r *lostJoinResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.blockRecovery && req.URL.Path == "/v1/credential" {
		return nil, errors.New("recovery temporarily offline")
	}

	response, err := r.base.RoundTrip(req)
	if err == nil && r.drop && (req.URL.Path == "/v1/session/join" || req.URL.Path == "/v1/connect/join") && response.StatusCode == http.StatusOK {
		r.drop = false
		response.Body.Close()
		return nil, errors.New("join response lost after commit")
	}

	return response, err
}

func TestRemoteCredentialDiscoveryHonorsDeadline(t *testing.T) {
	root := privateTestDir(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		conn, e := listener.Accept()
		if e == nil {
			<-done
			conn.Close()
		}
	}()
	defer func() { close(done); listener.Close() }()

	remoteFile, err := remotePath("stalled")
	if err != nil {
		t.Fatal(err)
	}

	if err = MkdirPrivate(filepath.Dir(remoteFile)); err != nil {
		t.Fatal(err)
	}

	if err = writePrivate(remoteFile, RemoteProfile{Name: "stalled", URL: "https://" + listener.Addr().String(), DatabaseID: opaqueID()}, false); err != nil {
		t.Fatal(err)
	}

	credential := filepath.Join(root, "agent.json")
	if err = writePrivate(credential, Credential{Kind: AgentCapability, Token: opaqueID(), Remote: "stalled"}, false); err != nil {
		t.Fatal(err)
	}

	deadline, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()

	_, err = NewClientContext(deadline, filepath.Join(root, "no-local-fallback"), credential, AgentCapability)
	if err == nil {
		t.Fatal("stalled TLS discovery succeeded")
	}

	if time.Since(start) > time.Second {
		t.Fatal("remote discovery exceeded the hook deadline")
	}

	if _, err = os.Stat(filepath.Join(root, "no-local-fallback")); !os.IsNotExist(err) {
		t.Fatal("discovery touched local fallback state")
	}
}

func TestRemoteJoinRecoversSavedCredential(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(map[bool]string{false: "session", true: "owner"}[owner], func(t *testing.T) {
			f := tasksFixture(t)
			const host = "server.example.ts.net"
			if err := f.service.SetServeHost(host); err != nil {
				t.Fatal(err)
			}

			f.service.Version = "test"
			backend, _ := url.Parse(f.service.Discovery.URL)
			proxy := httptest.NewTLSServer(&httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
				r.SetURL(backend)
				r.Out.Host = host
				r.Out.Header.Set("Tailscale-User-Login", "friend@example.com")
			}})
			defer proxy.Close()
			config := t.TempDir()
			t.Setenv("HOME", config)
			t.Setenv("XDG_CONFIG_HOME", config)
			t.Setenv("AppData", config)

			if _, err := connectRemote(ctx, "team", proxy.URL, proxy.Client()); err != nil {
				t.Fatal(err)
			}

			session := f.sessions[0]
			if owner {
				session = ""
			}

			issue := func(code string) error {
				if owner {
					return f.service.Store.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code))
				}

				_, err := f.service.Store.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(code)})
				return err
			}
			code := opaqueID()
			if err := issue(code); err != nil {
				t.Fatal(err)
			}

			path := filepath.Join(privateTestDir(t), "saved.json")
			transport := &lostJoinResponse{base: proxy.Client().Transport, drop: true, blockRecovery: true}

			client := &http.Client{Transport: transport}
			if _, err := joinRemoteCredentialUsing(ctx, "team", session, path, code, client); err == nil {
				t.Fatal("lost response plus offline recovery should fail")
			}

			saved, err := ReadCredential(path)
			if err != nil || saved.WorkspaceID != "" {
				t.Fatal("expected preliminary credential", err)
			}

			newCode := opaqueID()
			if err = issue(newCode); err != nil {
				t.Fatal("immediate fresh same-login invitation", err)
			}

			// A second device can enroll immediately; its new code must not revive the original one.
			second := filepath.Join(privateTestDir(t), "second.json")
			if _, err = joinRemoteCredentialUsing(ctx, "team", session, second, newCode, proxy.Client()); err != nil {
				t.Fatal("second device", err)
			}

			// Remove/expire the original retry row: success must come from bearer proof, not the old code.
			if owner {
				execTest(t, f.service.Store, "UPDATE owner_join_codes SET expires_at='2000-01-01T00:00:00Z'")
			}

			transport.blockRecovery = false

			ref, err := joinRemoteCredentialUsing(ctx, "team", session, path, code, client)
			if err != nil {
				t.Fatal("saved credential did not recover committed join", err)
			}

			if !owner && ref != f.ref(0) {
				t.Fatal("recovered wrong Session", ref)
			}

			finished, err := ReadCredential(path)
			if err != nil || finished.Token != saved.Token || finished.WorkspaceID != ref.WorkspaceID {
				t.Fatal("recovery did not finish original credential", err)
			}

			// Possessing a code or supplying a hash is not recovery; scope and kind must match too.
			otherCode, otherSecret := opaqueID(), opaqueID()

			_, err = f.service.Store.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(1), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(otherCode)})
			if err != nil {
				t.Fatal(err)
			}

			actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "friend@example.com"})
			if _, err = f.service.Store.JoinSession(actor, SessionJoinRequest{SessionID: f.sessions[1], Code: otherCode, CredentialHash: tokenHash(otherSecret)}); err != nil {
				t.Fatal(err)
			}

			for _, badToken := range []string{otherSecret, f.a.Token, opaqueID()} {
				bad := saved
				bad.Token = badToken
				if err = writePrivate(path, bad, true); err != nil {
					t.Fatal(err)
				}

				if _, err = joinRemoteCredentialUsing(ctx, "team", session, path, code, client); err == nil {
					t.Fatal("wrong-scope, wrong-kind or unknown bearer recovered")
				}
			}

			execTest(t, f.service.Store, "DELETE FROM capabilities WHERE token_hash=?", tokenHash(saved.Token))

			if err = writePrivate(path, saved, true); err != nil {
				t.Fatal(err)
			}

			if _, err = joinRemoteCredentialUsing(ctx, "team", session, path, code, client); err == nil {
				t.Fatal("revoked bearer recovered")
			}

			wrong := SessionJoinRequest{SessionID: session, Code: code, CredentialHash: tokenHash(opaqueID())}
			if owner {
				requireCode(t, f.service.Store.JoinOwner(actor, wrong), "forbidden")
			} else {
				_, err = f.service.Store.JoinSession(actor, wrong)
				requireCode(t, err, "forbidden")
			}
		})
	}
}
