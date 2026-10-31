package stack

import "testing"

func confirmedRecovery(t *testing.T, ref Context, id string) TransferChangeRequest {
	t.Helper()
	return TransferChangeRequest{Context: ref, TransferID: id, DestinationStopped: true}
}

func TestTransferOwnerRecovery(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "closed"}[closed], func(t *testing.T) {
			f := tasksFixture(t)
			execTest(t, f.service.Store, "UPDATE sessions SET closed=? WHERE id=?", closed, f.sessions[0])

			bundle, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
			if err != nil {
				t.Fatal(err)
			}

			request := confirmedRecovery(t, f.ref(0), bundle.TransferID)
			_, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", request)
			requireCode(t, err, "conflict")

			seal := TransferChangeRequest{Context: f.ref(0), TransferID: bundle.TransferID, DestinationDatabase: opaqueID()}
			for range 2 {
				if _, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "seal", seal); err != nil {
					t.Fatal(err)
				}
			}

			_, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", TransferChangeRequest{Context: f.ref(0), TransferID: bundle.TransferID})
			requireCode(t, err, "invalid_input")
			_, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", confirmedRecovery(t, f.ref(0), opaqueID()))
			requireCode(t, err, "not_found")
			_, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", confirmedRecovery(t, f.ref(1), bundle.TransferID))
			requireCode(t, err, "forbidden")
			execTest(t, f.service.Store, `CREATE TRIGGER reject_recovery BEFORE UPDATE OF state ON session_transfers WHEN NEW.state='recovered' BEGIN SELECT RAISE(ABORT,'injected recovery failure'); END;`)
			_, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", request)
			requireCode(t, err, "storage_unavailable")
			_, err = f.service.Store.InspectSession(ctx, f.operator.Token, f.ref(0))
			requireCode(t, err, "conflict")
			execTest(t, f.service.Store, "DROP TRIGGER reject_recovery")

			for range 2 {
				result, err := f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", request)
				if err != nil || result.State != "recovered" {
					t.Fatal(result, err)
				}

				session, err := f.service.Store.InspectSession(ctx, f.operator.Token, f.ref(0))
				if err != nil || session.Closed != closed {
					t.Fatal("original state not restored", err)
				}
			}

			// A lost-response retry must not overwrite changes made after recovery.
			execTest(t, f.service.Store, "UPDATE sessions SET closed=? WHERE id=?", !closed, f.sessions[0])

			if _, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", request); err != nil {
				t.Fatal(err)
			}

			session, err := f.service.Store.InspectSession(ctx, f.operator.Token, f.ref(0))
			if err != nil || session.Closed == closed {
				t.Fatal("retry replayed recovery", err)
			}

			_, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "seal", seal)
			requireCode(t, err, "conflict")

			fresh, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: bundle.Repository})
			if err != nil {
				t.Fatal(err)
			}

			if _, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "seal", TransferChangeRequest{Context: f.ref(0), TransferID: fresh.TransferID, DestinationDatabase: opaqueID()}); err != nil {
				t.Fatal(err)
			}

			_, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "recover", request)
			requireCode(t, err, "conflict")
		})
	}
}
