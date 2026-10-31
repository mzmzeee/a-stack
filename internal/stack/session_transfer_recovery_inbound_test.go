package stack

import "testing"

func TestTransferRecoveryRefusesInbound(t *testing.T) {
	source, dest := tasksFixture(t), tasksFixture(t)
	bundle, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
	if err != nil {
		t.Fatal(err)
	}

	staged, err := dest.service.Store.ImportTransfer(ctx, dest.operator.Token, TransferImportRequest{Bundle: bundle, AgentHashes: transferHashes(t, source, 0)})
	if err != nil {
		t.Fatal(err)
	}

	request := confirmedRecovery(t, staged.Context, bundle.TransferID)
	_, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "recover", request)
	requireCode(t, err, "conflict")

	if _, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: bundle.TransferID, DestinationDatabase: dest.service.Store.id}); err != nil {
		t.Fatal(err)
	}

	if _, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "activate", request); err != nil {
		t.Fatal(err)
	}

	_, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "recover", request)
	requireCode(t, err, "conflict")
}
