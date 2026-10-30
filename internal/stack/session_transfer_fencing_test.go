package stack

import "testing"

func TestCompetingTransfersFenceOldSource(t *testing.T) {
	source, dest := tasksFixture(t), tasksFixture(t)
	export := func() TransferBundle {
		b, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/repo"})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, b := export(), export()
	hashes := transferHashes(t, source, 0)
	ia, err := dest.service.Store.ImportTransfer(ctx, dest.operator.Token, TransferImportRequest{Bundle: a, AgentHashes: hashes})
	if err != nil {
		t.Fatal(err)
	}
	// A copied or forged replacement without the source's release cannot evict staging.
	forged := b
	forged.Released = nil
	_, err = dest.service.Store.ImportTransfer(ctx, dest.operator.Token, TransferImportRequest{Bundle: forged, AgentHashes: hashes})
	requireCode(t, err, "conflict")
	ib, err := dest.service.Store.ImportTransfer(ctx, dest.operator.Token, TransferImportRequest{Bundle: b, AgentHashes: hashes})
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: a.TransferID, DestinationDatabase: dest.service.Store.id})
	requireCode(t, err, "conflict")
	if _, err = source.service.Store.InspectSession(ctx, source.operator.Token, source.ref(0)); err != nil {
		t.Fatal("source must remain usable", err)
	}
	_, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "activate", TransferChangeRequest{Context: ia.Context, TransferID: a.TransferID})
	requireCode(t, err, "conflict")
	// Retrying B completes; retrying an old export never resurrects A.
	_, err = source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: a.TransferID, Repository: a.Repository})
	requireCode(t, err, "conflict")
	for i := 0; i < 2; i++ {
		_, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: b.TransferID, DestinationDatabase: dest.service.Store.id})
		if err != nil {
			t.Fatal(err)
		}
		_, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "activate", TransferChangeRequest{Context: ib.Context, TransferID: b.TransferID})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = dest.service.Store.InspectSession(ctx, dest.operator.Token, ib.Context); err != nil {
		t.Fatal(err)
	}
}
