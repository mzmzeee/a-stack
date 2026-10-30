package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestJoinCodesSingleUseAndScope(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	code := opaqueID()
	secret := opaqueID()
	actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "friend@example.com"})

	_, err := s.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(code)})
	if err != nil {
		t.Fatal(err)
	}

	join := SessionJoinRequest{SessionID: f.sessions[0], CredentialHash: tokenHash(secret)}
	for _, bad := range []string{"", opaqueID()} {
		join.Code = bad
		_, err = s.JoinSession(actor, join)
		requireCode(t, err, "forbidden")
	}

	join.Code = code
	wrong := join
	wrong.SessionID = f.sessions[1]
	_, err = s.JoinSession(actor, wrong)
	requireCode(t, err, "forbidden")

	for i := 0; i < 2; i++ {
		if _, err = s.JoinSession(actor, join); err != nil {
			t.Fatal(err)
		}
	}

	wrong = join
	wrong.CredentialHash = tokenHash(opaqueID())
	_, err = s.JoinSession(actor, wrong)
	requireCode(t, err, "forbidden")
	err = s.CreateOwnerCode(ctx, secret, tokenHash(opaqueID()))
	requireCode(t, err, "forbidden")

	if _, err = s.db.Exec("UPDATE session_invites SET expires_at='2000-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}

	_, err = s.JoinSession(actor, join)
	requireCode(t, err, "forbidden")
}

func TestOwnerCodesSingleUseAndIdentity(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	code := opaqueID()
	secret := opaqueID()

	if err := s.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code)); err != nil {
		t.Fatal(err)
	}

	join := SessionJoinRequest{Code: code, CredentialHash: tokenHash(secret)}
	requireCode(t, s.JoinOwner(ctx, join), "forbidden")
	actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "owner@example.com"})
	wrong := join
	wrong.Code = ""
	requireCode(t, s.JoinOwner(actor, wrong), "forbidden")

	for i := 0; i < 2; i++ {
		if err := s.JoinOwner(actor, join); err != nil {
			t.Fatal(err)
		}
	}

	other := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "other@example.com"})
	requireCode(t, s.JoinOwner(other, join), "forbidden")
	wrong = join
	wrong.CredentialHash = tokenHash(opaqueID())
	requireCode(t, s.JoinOwner(actor, wrong), "forbidden")

	if _, err := s.InspectSession(ctx, secret, f.ref(1)); err != nil {
		t.Fatal("owner did not enroll", err)
	}

	if _, err := s.db.Exec("UPDATE owner_join_codes SET expires_at='2000-01-01T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}

	requireCode(t, s.JoinOwner(actor, join), "forbidden")
}

func TestForgedLoopbackCannotEnrollWithoutCode(t *testing.T) {
	f := tasksFixture(t)
	_, err := f.service.Store.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(opaqueID())})
	if err != nil {
		t.Fatal(err)
	}

	if err = f.service.SetServeHost("server.example.ts.net"); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/v1/session/join", "/v1/connect/join"} {
		secret := opaqueID()

		request := SessionJoinRequest{CredentialHash: tokenHash(secret)}
		if path == "/v1/session/join" {
			request.SessionID = f.sessions[0]
		}

		body, _ := json.Marshal(request)
		r, _ := http.NewRequest("POST", f.service.Discovery.URL+path, bytes.NewReader(body))
		r.Host = "server.example.ts.net"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-A-Stack-Instance", f.service.Discovery.InstanceID)
		r.Header.Set("Tailscale-User-Login", "friend@example.com")

		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}

		response.Body.Close()

		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: status %d", path, response.StatusCode)
		}

		if _, err = f.service.Store.InspectSession(ctx, secret, f.ref(0)); err == nil {
			t.Fatal("forged login enrolled")
		}
	}
}
