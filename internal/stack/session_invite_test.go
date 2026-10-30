package stack

import (
	"context"
	"testing"
)

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
