package stack

import (
	"context"
	"fmt"
	"testing"
)

func TestSessionInvitationLimitAndCleanup(t *testing.T) {
	f := tasksFixture(t)
	s := f.service.Store
	code, secret := opaqueID(), opaqueID()

	_, err := s.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(code)})
	if err != nil {
		t.Fatal(err)
	}

	actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "friend@example.com"})
	join := SessionJoinRequest{SessionID: f.sessions[0], Code: code, CredentialHash: tokenHash(secret)}
	if _, err = s.JoinSession(actor, join); err != nil {
		t.Fatal(err)
	}

	successes := 0

	var first SessionInviteRequest

	before := countTest(t, s, "SELECT count(*) FROM lifecycle_history")
	receiptsBefore := countTest(t, s, "SELECT count(*) FROM operation_receipts")
	for i := 0; i < 100; i++ {
		request := SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: fmt.Sprintf("guest-%d@example.com", i), CodeHash: tokenHash(opaqueID())}
		if i == 0 {
			first = request
		}

		_, err = s.InviteSession(ctx, secret, request)
		if i < 16 {
			if err != nil {
				t.Fatal(err)
			}

			successes++
		} else {
			requireCode(t, err, "rate_limited")
		}
	}

	if successes != 16 || countTest(t, s, "SELECT count(*) FROM session_invites WHERE consumed_hash=''") != 16 || countTest(t, s, "SELECT count(*) FROM session_invites") != 17 {
		t.Fatal("active invitation cap or consumed recovery row lost")
	}

	if got := countTest(t, s, "SELECT count(*) FROM lifecycle_history") - before; got != 16 {
		t.Fatal("rejected invitations wrote history", got)
	}

	if got := countTest(t, s, "SELECT count(*) FROM operation_receipts") - receiptsBefore; got != 16 {
		t.Fatal("rejected invitations wrote receipts", got)
	}

	// An exact operation retry and a replacement for the same login work at capacity.
	if _, err = s.InviteSession(ctx, secret, first); err != nil {
		t.Fatal("invite retry", err)
	}

	first.OperationID = opaqueID()
	first.CodeHash = tokenHash(opaqueID())
	if _, err = s.InviteSession(ctx, secret, first); err != nil {
		t.Fatal("replace at capacity", err)
	}

	// Issuance preserves both enrolled credentials and exact lost-response recovery.
	if _, err = s.InspectSession(ctx, secret, f.ref(0)); err != nil {
		t.Fatal(err)
	}

	if _, err = s.JoinSession(actor, join); err != nil {
		t.Fatal("unrelated invitations lost join recovery", err)
	}

	_, err = s.InviteSession(ctx, secret, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(opaqueID())})
	requireCode(t, err, "conflict")

	if _, err = s.JoinSession(actor, join); err != nil {
		t.Fatal("same-login reissue lost join recovery", err)
	}

	// Legacy expired rows are cleaned in the same transaction as a fresh invitation.
	execTest(t, s, "UPDATE session_invites SET expires_at='2000-01-01T00:00:00Z'")

	for i := 0; i < 100; i++ {
		execTest(t, s, "INSERT INTO session_invites(session_id,login,code_hash,expires_at) VALUES(?,?,?,'2000-01-01T00:00:00Z')", f.sessions[0], fmt.Sprintf("expired-%d@example.com", i), tokenHash(opaqueID()))
	}

	first.OperationID = opaqueID()
	execTest(t, s, "CREATE TEMP TRIGGER fail_invite BEFORE INSERT ON session_invites BEGIN SELECT RAISE(ABORT,'injected'); END")

	if _, err = s.InviteSession(ctx, secret, first); err == nil {
		t.Fatal("injected issuance failure ignored")
	}

	if countTest(t, s, "SELECT count(*) FROM session_invites") != 117 {
		t.Fatal("failed issuance committed cleanup")
	}

	execTest(t, s, "DROP TRIGGER fail_invite")

	if _, err = s.InviteSession(ctx, secret, first); err != nil {
		t.Fatal(err)
	}

	if countTest(t, s, "SELECT count(*) FROM session_invites") != 1 {
		t.Fatal("expired invitations retained")
	}
}

func TestSessionInvitationScope(t *testing.T) {
	f := tasksFixture(t)

	s := f.service.Store
	invite := SessionInviteRequest{CodeHash: tokenHash("test-join-code"), Context: f.ref(0), OperationID: opaqueID(), Login: "friend@example.com"}
	if _, err := s.InviteSession(ctx, f.operator.Token, invite); err != nil {
		t.Fatal(err)
	}

	secret := opaqueID()
	join := SessionJoinRequest{Code: "test-join-code", SessionID: f.sessions[0], CredentialHash: tokenHash(secret)}
	_, err := s.JoinSession(ctx, join)
	requireCode(t, err, "forbidden")
	wrong := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "other@example.com"})
	_, err = s.JoinSession(wrong, join)
	requireCode(t, err, "forbidden")
	friend := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: invite.Login})

	ref, err := s.JoinSession(friend, join)
	if err != nil || ref != f.ref(0) {
		t.Fatal(ref, err)
	}

	if _, err = s.JoinSession(friend, join); err != nil {
		t.Fatal("join retry", err)
	}

	if _, err = s.InspectSession(ctx, secret, f.ref(0)); err != nil {
		t.Fatal(err)
	}

	_, err = s.InspectSession(ctx, secret, f.ref(1))
	requireCode(t, err, "forbidden")

	page, err := s.ListLifecycle(ctx, secret, "session.list", ListRequest{})
	if err != nil || len(page.(Page[Session]).Items) != 1 {
		t.Fatal(page, err)
	}

	_, err = s.ChangeOrders(ctx, secret, OrdersRequest{WorkspaceID: f.ref(0).WorkspaceID, OperationID: opaqueID(), ExpectedRevision: 1, Body: "unauthorized"})
	requireCode(t, err, "forbidden")

	if _, err = s.ReadOrders(ctx, secret, f.ref(0)); err != nil {
		t.Fatal(err)
	}

	_, err = s.CreateSession(ctx, secret, SessionCreateRequest{WorkspaceID: f.ref(0).WorkspaceID, OperationID: opaqueID(), Name: "uninvited"})
	requireCode(t, err, "forbidden")
	_, err = s.RevokeViews(ctx, secret, ViewRevokeRequest{All: true})
	requireCode(t, err, "forbidden")

	if _, err = s.InviteSession(ctx, secret, SessionInviteRequest{CodeHash: tokenHash("test-join-code"), Context: f.ref(0), OperationID: opaqueID(), Login: "next@example.com"}); err != nil {
		t.Fatal(err)
	}

	_, err = s.InviteSession(ctx, secret, SessionInviteRequest{CodeHash: tokenHash("test-join-code"), Context: f.ref(1), OperationID: opaqueID(), Login: "next@example.com"})
	requireCode(t, err, "forbidden")

	session, err := s.InspectSession(ctx, secret, f.ref(0))
	if err != nil {
		t.Fatal(err)
	}

	closeRequest := SessionChangeRequest{Context: f.ref(0), OperationID: opaqueID(), ExpectedRevision: session.Revision}

	receipt, err := s.ChangeSession(ctx, secret, "close", closeRequest)
	if err != nil {
		t.Fatal(err)
	}

	closeRequest.ExpectedRevision = receipt.Revision
	closeRequest.OperationID = opaqueID()
	if _, err = s.ChangeSession(ctx, secret, "reopen", closeRequest); err != nil {
		t.Fatal(err)
	}
}
