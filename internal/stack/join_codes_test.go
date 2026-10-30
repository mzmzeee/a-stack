package stack

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
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

func TestJoinRecoveryAfterUnrelatedIssuance(t *testing.T) {
	for _, owner := range []bool{false, true} {
		t.Run(map[bool]string{false: "session", true: "owner"}[owner], func(t *testing.T) {
			f := tasksFixture(t)
			s := f.service.Store
			code, secret := opaqueID(), opaqueID()
			actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "first@example.com"})
			issue := func(code, login string) error {
				if owner {
					return s.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code))
				}

				_, err := s.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: login, CodeHash: tokenHash(code)})
				return err
			}

			join := SessionJoinRequest{Code: code, CredentialHash: tokenHash(secret)}
			if !owner {
				join.SessionID = f.sessions[0]
			}

			retry := func(actor context.Context, request SessionJoinRequest) error {
				if owner {
					return s.JoinOwner(actor, request)
				}

				_, err := s.JoinSession(actor, request)
				return err
			}

			if err := issue(code, "first@example.com"); err != nil {
				t.Fatal(err)
			}

			if err := retry(actor, join); err != nil {
				t.Fatal(err)
			}

			unusedCode := opaqueID()
			if err := issue(unusedCode, "second@example.com"); err != nil {
				t.Fatal(err)
			}

			if err := retry(actor, join); err != nil {
				t.Error("exact lost-response join retry lost after unrelated issuance", err)
			}

			table := "session_invites"
			if owner {
				table = "owner_join_codes"
			}

			// A committed join can recover for 24 hours after expiry, but an unused code cannot enroll then.
			expired := clockNow().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
			execTest(t, s, "UPDATE "+table+" SET expires_at=?", expired)
			unused := join
			unused.Code = unusedCode
			unused.CredentialHash = tokenHash(opaqueID())
			secondActor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "second@example.com"})
			requireCode(t, retry(secondActor, unused), "forbidden")

			if err := retry(actor, join); err != nil {
				t.Error("exact retry inside post-expiry recovery window", err)
			}

			wrong := join
			wrong.CredentialHash = tokenHash(opaqueID())
			requireCode(t, retry(actor, wrong), "forbidden")
			other := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "other@example.com"})
			requireCode(t, retry(other, join), "forbidden")

			if err := issue(opaqueID(), "third@example.com"); err != nil {
				t.Fatal(err)
			}

			if err := retry(actor, join); err != nil {
				t.Error("cleanup removed recovery record", err)
			}

			if owner {
				requireCode(t, issue(code, "first@example.com"), "conflict")
				requireCode(t, retry(other, wrong), "forbidden")
			}

			execTest(t, s, "UPDATE "+table+" SET expires_at=?", clockNow().UTC().Add(-25*time.Hour).Format(time.RFC3339Nano))
			requireCode(t, retry(actor, join), "forbidden")

			if err := issue(opaqueID(), "fourth@example.com"); err != nil {
				t.Fatal(err)
			}

			if owner {
				requireCode(t, issue(code, "first@example.com"), "conflict")
			}
		})
	}
}

func TestOwnerCodeRetirement(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	for i := 0; i < 100; i++ {
		execTest(t, s, "INSERT INTO owner_join_codes(code_hash,expires_at) VALUES(?,'2000-01-01T00:00:00Z')", tokenHash(opaqueID()))
	}

	code, secret := opaqueID(), opaqueID()
	if err := s.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code)); err != nil {
		t.Fatal(err)
	}

	if countTest(t, s, "SELECT count(*) FROM owner_join_codes") != 101 {
		t.Fatal("expired owner-code retirement records lost")
	}

	actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "owner@example.com"})
	join := SessionJoinRequest{Code: code, CredentialHash: tokenHash(secret)}
	if err := s.JoinOwner(actor, join); err != nil {
		t.Fatal(err)
	}

	requireCode(t, s.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code)), "conflict")

	if err := s.JoinOwner(actor, join); err != nil {
		t.Fatal("same-code issuance must not reset used code", err)
	}

	if err := s.CreateOwnerCode(ctx, f.operator.Token, tokenHash(opaqueID())); err != nil {
		t.Fatal(err)
	}

	if countTest(t, s, "SELECT count(*) FROM owner_join_codes") != 102 {
		t.Fatal("used owner-code retirement record lost")
	}

	// A, consume A, issue B, replay issuance A: A must never authorize another principal.
	requireCode(t, s.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code)), "conflict")
	other := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "different@example.com"})
	wrong := join
	wrong.CredentialHash = tokenHash(opaqueID())
	requireCode(t, s.JoinOwner(other, wrong), "forbidden")

	if err := s.JoinOwner(actor, join); err != nil {
		t.Fatal("unrelated issuance lost exact owner recovery", err)
	}

	if _, err := s.InspectSession(ctx, secret, f.ref(0)); err != nil {
		t.Fatal("cleanup revoked enrolled owner", err)
	}
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
