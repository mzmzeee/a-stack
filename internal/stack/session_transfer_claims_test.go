package stack

import "testing"

func TestTransferInvalidatesWorkspaceClaims(t *testing.T) {
	source, dest := tasksFixture(t), tasksFixture(t)
	execTest(t, dest.service.Store, "UPDATE agents SET name='dest-' || name")
	claim := func(f taskTeam) string {
		t.Helper()

		task := f.create(t, "claim")
		assigned := f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: task.ResourceID, ExpectedRevision: task.Revision, AgentID: f.aID})
		if _, err := f.service.Store.ChangeClaims(ctx, f.a.Token, false, ClaimRequest{Context: f.ref(0), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: assigned.Revision, Paths: []string{"src"}}); err != nil {
			t.Fatal(err)
		}

		return task.ResourceID
	}
	sourceTask, destTask := claim(source), claim(dest)
	other, agent := source.register(t, 1, "other-orch")
	source.appoint(t, 1, agent.ResourceID, 1)

	task, err := source.service.Store.CreateTask(ctx, other.Token, TaskCreateRequest{Context: source.ref(1), OperationID: opaqueID(), Title: "other", AgentIDs: []string{agent.ResourceID}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = source.service.Store.ChangeClaims(ctx, other.Token, false, ClaimRequest{Context: source.ref(1), TaskID: task.ResourceID, OperationID: opaqueID(), ExpectedRevision: task.Revision, Paths: []string{"src/x"}}); err != nil {
		t.Fatal(err)
	}

	inspect := func(f taskTeam, session int) Session {
		t.Helper()

		s, e := f.service.Store.InspectSession(ctx, f.operator.Token, f.ref(session))
		if e != nil {
			t.Fatal(e)
		}

		return s
	}
	check := func(f taskTeam, ref Context, id string, count int) {
		t.Helper()

		d, e := f.service.Store.ShowTask(ctx, f.operator.Token, TaskReadRequest{Context: ref, TaskID: id})
		if e != nil || len(d.ClaimWarnings) != count {
			t.Fatalf("warnings: %+v %v", d.ClaimWarnings, e)
		}
	}
	check(source, source.ref(1), task.ResourceID, 1)
	check(dest, dest.ref(0), destTask, 0)

	snapshot, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/team/claims"})
	if err != nil {
		t.Fatal(err)
	}

	var path string
	if err = dest.service.Store.db.QueryRow("SELECT path FROM workspaces WHERE id=?", dest.ref(0).WorkspaceID).Scan(&path); err != nil {
		t.Fatal(err)
	}

	imported, err := dest.service.Store.ImportTransfer(ctx, dest.operator.Token, TransferImportRequest{Bundle: snapshot, AgentHashes: transferHashes(t, source, 0), WorkspacePath: path})
	if err != nil {
		t.Fatal(err)
	}

	check(dest, dest.ref(0), destTask, 0)

	before := inspect(source, 1)
	if _, err = source.service.Store.ChangeTransfer(ctx, source.operator.Token, "seal", TransferChangeRequest{Context: source.ref(0), TransferID: snapshot.TransferID, DestinationDatabase: dest.service.Store.id}); err != nil {
		t.Fatal(err)
	}

	if after := inspect(source, 1); after.ReadRevision <= before.ReadRevision {
		t.Error("seal left another Session claim cache stale")
	}

	check(source, source.ref(1), task.ResourceID, 0)

	before = inspect(dest, 0)
	activate := TransferChangeRequest{Context: imported.Context, TransferID: snapshot.TransferID}
	if _, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "activate", activate); err != nil {
		t.Fatal(err)
	}

	after := inspect(dest, 0)
	if after.ReadRevision <= before.ReadRevision {
		t.Error("activation left another Session claim cache stale")
	}

	check(dest, dest.ref(0), destTask, 1)
	check(dest, imported.Context, sourceTask, 1)

	if _, err = dest.service.Store.ChangeTransfer(ctx, dest.operator.Token, "activate", activate); err != nil {
		t.Fatal(err)
	}

	if again := inspect(dest, 0); again.ReadRevision != after.ReadRevision {
		t.Error("activation retry invalidated claims again")
	}
}
