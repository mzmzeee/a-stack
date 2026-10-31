package stack

import (
	"context"
	"testing"
)

func TestTransferSealRejectsUnprovedSessionOperator(t *testing.T) {
	for _, caller := range []string{"invited", "other-session", "agent", "stranger"} {
		t.Run(caller, func(t *testing.T) {
			f := tasksFixture(t)
			token := opaqueID()
			switch caller {
			case "invited", "other-session":
				index := 0
				if caller == "other-session" {
					index = 1
				}

				code := opaqueID()

				_, err := f.service.Store.InviteSession(ctx, f.operator.Token, SessionInviteRequest{Context: f.ref(index), OperationID: opaqueID(), Login: "friend@example.com", CodeHash: tokenHash(code)})
				if err != nil {
					t.Fatal(err)
				}

				actor := context.WithValue(ctx, requestActorKey{}, RequestActor{Login: "friend@example.com"})
				if _, err = f.service.Store.JoinSession(actor, SessionJoinRequest{SessionID: f.sessions[index], Code: code, CredentialHash: tokenHash(token)}); err != nil {
					t.Fatal(err)
				}
			case "agent":
				token = f.a.Token
			}

			bundle, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
			if err != nil {
				t.Fatal(err)
			}

			_, err = f.service.Store.ChangeTransfer(ctx, token, "seal", TransferChangeRequest{Context: f.ref(0), TransferID: bundle.TransferID, DestinationDatabase: opaqueID()})
			if caller != "stranger" {
				requireCode(t, err, "forbidden")
			} else if err == nil {
				t.Fatal("stranger sealed")
			}

			if _, err = f.service.Store.InspectSession(ctx, f.operator.Token, f.ref(0)); err != nil {
				t.Fatal("failed cutover orphaned source", err)
			}

			if _, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "seal", TransferChangeRequest{Context: f.ref(0), TransferID: bundle.TransferID, DestinationDatabase: opaqueID()}); err != nil {
				t.Fatal(err)
			}

			_, err = f.service.Store.ChangeTransfer(ctx, token, "recover", confirmedRecovery(t, f.ref(0), bundle.TransferID))
			if caller != "stranger" {
				requireCode(t, err, "forbidden")
			} else if err == nil {
				t.Fatal("stranger recovered")
			}
		})
	}
}
