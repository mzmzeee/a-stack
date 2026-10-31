package stack

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestJoinCodeNanosecondExpiry(t *testing.T) {
	for _, owner := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			for _, fraction := range []time.Duration{0, 100 * time.Millisecond} {
				for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
					t.Run(fmt.Sprintf("owner=%v/legacy=%v/fraction=%d/delta=%d", owner, legacy, fraction, delta), func(t *testing.T) {
						f := tasksFixture(t)
						at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Add(fraction)
						fixedClock.Store(&at)
						t.Cleanup(func() { fixedClock.Store(nil) })

						code := opaqueID()
						if owner {
							if err := f.service.Store.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code)); err != nil {
								t.Fatal(err)
							}
						} else {
							if _, err := f.service.Store.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(code)}); err != nil {
								t.Fatal(err)
							}
						}

						expiry := at.Add(24 * time.Hour)
						if legacy {
							if owner {
								execTest(t, f.service.Store, "UPDATE owner_join_codes SET expires_at=?", expiry.Format(time.RFC3339Nano))
							} else {
								execTest(t, f.service.Store, "UPDATE session_invites SET expires_at=?", expiry.Format(time.RFC3339Nano))
							}
						}

						at = expiry.Add(delta)
						actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "friend@example.com"})

						request := SessionJoinRequest{Code: code, CredentialHash: tokenHash(opaqueID())}
						var err error
						if owner {
							err = f.service.Store.JoinOwner(actor, request)
						} else {
							request.SessionID = f.sessions[0]
							_, err = f.service.Store.JoinSession(actor, request)
						}

						if delta < 0 {
							if err != nil {
								t.Fatal("unexpired code denied", err)
							}
						} else {
							requireCode(t, err, "forbidden")
						}

						if owner && delta >= 0 {
							requireCode(t, f.service.Store.CreateOwnerCode(ctx, f.operator.Token, tokenHash(code)), "conflict")
						}

						if !owner && delta >= 0 {
							_, err = f.service.Store.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "fresh@example.com", CodeHash: tokenHash(opaqueID())})
							if err != nil {
								t.Fatal(err)
							}

							if countTest(t, f.service.Store, "SELECT count(*) FROM session_invites") != 1 {
								t.Fatal("expired invitation survived cleanup")
							}
						}
					})
				}
			}
		}
	}
}
