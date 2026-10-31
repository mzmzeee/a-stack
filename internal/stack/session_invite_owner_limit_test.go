package stack

import (
	"context"
	"fmt"
	"testing"
)

func TestOwnerInvitationsDoNotUseFriendCapacity(t *testing.T) {
	f := tasksFixture(t)

	friend, code := opaqueID(), opaqueID()
	invite := func(token, login, code string) error {
		_, err := f.service.Store.InviteSession(ctx, token, SessionInviteRequest{Context: f.ref(0), OperationID: opaqueID(), Login: login, CodeHash: tokenHash(code)})
		return err
	}
	if err := invite(f.operator.Token, "friend@example.com", code); err != nil {
		t.Fatal(err)
	}

	actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "friend@example.com"})
	if _, err := f.service.Store.JoinSession(actor, SessionJoinRequest{SessionID: f.sessions[0], Code: code, CredentialHash: tokenHash(friend)}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 20; i++ {
		if err := invite(f.operator.Token, fmt.Sprintf("owner-%d@example.com", i), opaqueID()); err != nil {
			t.Fatal("owner limited", err)
		}
	}

	for i := 0; i < 16; i++ {
		if err := invite(friend, fmt.Sprintf("guest-%d@example.com", i), opaqueID()); err != nil {
			t.Fatal("owner invitations consumed friend capacity", err)
		}
	}

	requireCode(t, invite(friend, "overflow@example.com", opaqueID()), "rate_limited")
	// Replacing an owner invitation must consume a friend slot, even for the same login.
	requireCode(t, invite(friend, "owner-0@example.com", opaqueID()), "rate_limited")

	ownerCode := opaqueID()
	if err := invite(f.operator.Token, "owner-extra@example.com", ownerCode); err != nil {
		t.Fatal("friends blocked owner", err)
	}

	if err := invite(f.operator.Token, "guest-0@example.com", opaqueID()); err != nil {
		t.Fatal(err)
	}

	if err := invite(friend, "overflow@example.com", opaqueID()); err != nil {
		t.Fatal("owner replacement did not free friend capacity", err)
	}

	// Issuer classification and codes survive a transfer.
	bundle, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
	if err != nil {
		t.Fatal(err)
	}

	dest := tasksFixture(t)
	imported, err := dest.service.Store.ImportTransfer(ctx, dest.operator.Token, TransferImportRequest{Bundle: bundle, AgentHashes: transferHashes(t, f, 0)})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "activate", TransferChangeRequest{Context: imported.Context, TransferID: bundle.TransferID}); err != nil {
		t.Fatal(err)
	}

	ownerActor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "owner-extra@example.com"})
	join := SessionJoinRequest{SessionID: imported.SessionID, Code: ownerCode, CredentialHash: tokenHash(opaqueID())}
	for range 2 {
		if _, err = dest.service.Store.JoinSession(ownerActor, join); err != nil {
			t.Fatal("owner invitation lost at transfer", err)
		}
	}
}
