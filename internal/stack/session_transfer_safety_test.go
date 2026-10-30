package stack

import "testing"

func TestTransferRejectsDestinationReferences(t *testing.T) {
	source, destination := tasksFixture(t), tasksFixture(t)
	source.create(t, "scoped data")

	bundle, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := source.service.Store.db.QueryContext(ctx, "SELECT * FROM tasks LIMIT 0")
	if err != nil {
		t.Fatal(err)
	}

	columns, err := rows.Columns()
	rows.Close()

	if err != nil {
		t.Fatal(err)
	}

	for i, table := range bundle.Tables {
		if table.Name == "tasks" {
			for j, column := range columns {
				if column == "created_by" {
					bundle.Tables[i].Rows[0][j] = destination.orchID
				}
			}
		}
	}

	_, err = destination.service.Store.ImportTransfer(ctx, destination.operator.Token, TransferImportRequest{Bundle: bundle, AgentHashes: transferHashes(t, source, 0)})
	requireCode(t, err, "invalid_input")

	if countTest(t, destination.service.Store, "SELECT count(*) FROM sessions WHERE id='"+source.sessions[0]+"'") != 0 {
		t.Fatal("out-of-scope import committed")
	}
}

func TestTransferDetectsHistoryOnlyWrite(t *testing.T) {
	source := tasksFixture(t)
	bundle, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = source.service.Store.OpenBrowser(ctx, source.operator.Token, BrowserOpenRequest{Context: source.ref(0), OperationID: opaqueID(), CredentialHash: tokenHash(opaqueID())}); err != nil {
		t.Fatal(err)
	}

	_, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: bundle.TransferID, DestinationDatabase: opaqueID()})
	requireCode(t, err, "conflict")

	if _, err = source.service.Store.InspectSession(ctx, source.operator.Token, source.ref(0)); err != nil {
		t.Fatal("history-only change sealed the source", err)
	}
}

func TestTransferSealStorageFailureRollsBack(t *testing.T) {
	source := tasksFixture(t)
	bundle, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
	if err != nil {
		t.Fatal(err)
	}

	execTest(t, source.service.Store, `CREATE TRIGGER reject_transfer_seal BEFORE UPDATE OF closed ON sessions WHEN NEW.closed=1 BEGIN SELECT RAISE(ABORT,'injected seal failure'); END;`)
	_, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: bundle.TransferID, DestinationDatabase: opaqueID()})
	requireCode(t, err, "storage_unavailable")

	if _, err = source.service.Store.InspectSession(ctx, source.operator.Token, source.ref(0)); err != nil {
		t.Fatal("partial seal committed", err)
	}

	status, err := source.service.Store.ChangeTransfer(ctx, source.operator.Token, "status", TransferChangeRequest{Context: source.ref(0), TransferID: bundle.TransferID})
	if err != nil || status.State != "prepared" {
		t.Fatal(status, err)
	}
}

func TestTransferSupersedesOnlyInactiveSnapshot(t *testing.T) {
	source := tasksFixture(t)
	destination := tasksFixture(t)
	export := func() TransferBundle {
		b, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
		if err != nil {
			t.Fatal(err)
		}

		return b
	}
	old := export()

	hashes := transferHashes(t, source, 0)
	first, err := destination.service.Store.ImportTransfer(ctx, destination.operator.Token, TransferImportRequest{Bundle: old, AgentHashes: hashes})
	if err != nil {
		t.Fatal(err)
	}

	source.create(t, "changed while staging")
	fresh := export()

	second, err := destination.service.Store.ImportTransfer(ctx, destination.operator.Token, TransferImportRequest{Bundle: fresh, AgentHashes: hashes})
	if err != nil {
		t.Fatal(err)
	}

	_, err = destination.service.Store.ChangeTransfer(ctx, destination.operator.Token, "activate", TransferChangeRequest{Context: first.Context, TransferID: old.TransferID})
	requireCode(t, err, "conflict")
	_, err = destination.service.Store.InspectSession(ctx, destination.operator.Token, second.Context)
	requireCode(t, err, "conflict")

	if _, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: fresh.TransferID, DestinationDatabase: destination.service.Store.id}); err != nil {
		t.Fatal(err)
	}

	if _, err = destination.service.Store.ChangeTransfer(ctx, destination.operator.Token, "activate", TransferChangeRequest{Context: second.Context, TransferID: fresh.TransferID}); err != nil {
		t.Fatal(err)
	}
}
