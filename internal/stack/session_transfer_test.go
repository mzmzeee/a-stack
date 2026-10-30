package stack

import "testing"

func transferHashes(t *testing.T, f taskTeam, session int) map[string]string {
	t.Helper()

	page, err := f.service.Store.ListLifecycle(ctx, f.operator.Token, "agent.list", ListRequest{Context: f.ref(session)})
	if err != nil {
		t.Fatal(err)
	}

	hashes := map[string]string{}
	for _, agent := range page.(Page[Agent]).Items {
		hashes[agent.ID] = tokenHash(opaqueID())
	}

	return hashes
}

func TestSessionTransferRoundTrip(t *testing.T) {
	source := tasksFixture(t)
	destination := tasksFixture(t)
	task := source.create(t, "keep this report and graph")
	const readAt = "2026-10-01T12:00:00Z"
	execTest(t, source.service.Store, "UPDATE agents SET last_mail_read_at=? WHERE session_id=?", readAt, source.sessions[0])

	snapshot, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/team/repo"})
	if err != nil {
		t.Fatal(err)
	}

	input := TransferImportRequest{Bundle: snapshot, AgentHashes: transferHashes(t, source, 0)}
	imported, err := destination.service.Store.ImportTransfer(ctx, destination.operator.Token, input)
	if err != nil {
		t.Fatal("import", err)
	}

	if imported.State != "staged" {
		t.Fatal(imported)
	}

	var retainedReads int
	if err = destination.service.Store.db.QueryRow("SELECT count(*) FROM agents WHERE session_id=? AND last_mail_read_at=?", imported.SessionID, readAt).Scan(&retainedReads); err != nil || retainedReads != len(input.AgentHashes) {
		t.Fatal("transfer lost own-read evidence", retainedReads, err)
	}

	_, err = destination.service.Store.InspectSession(ctx, destination.operator.Token, imported.Context)
	requireCode(t, err, "conflict")

	if _, err = source.service.Store.InspectSession(ctx, source.operator.Token, source.ref(0)); err != nil {
		t.Fatal("source must remain open before cutover", err)
	}

	sealed, err := source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: snapshot.TransferID, DestinationDatabase: destination.service.Store.id, Remote: "team"})
	if err != nil || sealed.State != "sealed" {
		t.Fatal(sealed, err)
	}

	_, err = source.service.Store.InspectSession(ctx, source.operator.Token, source.ref(0))
	requireCode(t, err, "conflict")
	_, err = source.service.Store.ChangeSession(ctx, source.operator.Token, "reopen", SessionChangeRequest{Context: source.ref(0), OperationID: opaqueID(), ExpectedRevision: snapshot.Session.Revision})
	requireCode(t, err, "conflict")

	active, err := destination.service.Store.ChangeTransfer(ctx, destination.operator.Token, "activate", TransferChangeRequest{Context: imported.Context, TransferID: snapshot.TransferID})
	if err != nil || active.State != "active" {
		t.Fatal(active, err)
	}

	if _, err = destination.service.Store.ImportTransfer(ctx, destination.operator.Token, input); err != nil {
		t.Fatal("lost import response retry", err)
	}

	got, err := destination.service.Store.ShowTask(ctx, destination.operator.Token, TaskReadRequest{Context: active.Context, TaskID: task.ResourceID})
	if err != nil || got.Title != "keep this report and graph" {
		t.Fatal(got, err)
	}

	back, err := destination.service.Store.ExportTransfer(ctx, destination.operator.Token, TransferExportRequest{Context: active.Context, TransferID: opaqueID(), Repository: snapshot.Repository})
	if err != nil {
		t.Fatal("reverse export", err)
	}

	hashes := map[string]string{}
	for agent := range input.AgentHashes {
		hashes[agent] = tokenHash(opaqueID())
	}

	local, err := source.service.Store.ImportTransfer(ctx, source.operator.Token, TransferImportRequest{Bundle: back, AgentHashes: hashes})
	if err != nil {
		t.Fatal("reverse import", err)
	}

	if _, err = destination.service.Store.ChangeTransfer(ctx, destination.operator.Token, "seal", TransferChangeRequest{Context: active.Context, TransferID: back.TransferID, DestinationDatabase: source.service.Store.id}); err != nil {
		t.Fatal(err)
	}

	if _, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "activate", TransferChangeRequest{Context: local.Context, TransferID: back.TransferID}); err != nil {
		t.Fatal(err)
	}

	if _, err = source.service.Store.ShowTask(ctx, source.operator.Token, TaskReadRequest{Context: local.Context, TaskID: task.ResourceID}); err != nil {
		t.Fatal(err)
	}
}

func TestTransferFailureKeepsSource(t *testing.T) {
	source := tasksFixture(t)
	destination := tasksFixture(t)
	source.create(t, "before")

	snapshot, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/team/repo"})
	if err != nil {
		t.Fatal(err)
	}

	bad := TransferImportRequest{Bundle: snapshot, AgentHashes: map[string]string{}}
	if _, err = destination.service.Store.ImportTransfer(ctx, destination.operator.Token, bad); err == nil {
		t.Fatal("missing credentials accepted")
	}

	if countTest(t, destination.service.Store, "SELECT count(*) FROM sessions WHERE id='"+source.sessions[0]+"'") != 0 {
		t.Fatal("failed import leaked Session")
	}

	source.create(t, "concurrent write")
	_, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: snapshot.TransferID, DestinationDatabase: destination.service.Store.id})
	requireCode(t, err, "conflict")

	if _, err = source.service.Store.InspectSession(ctx, source.operator.Token, source.ref(0)); err != nil {
		t.Fatal("source was sealed on conflict", err)
	}
}
