package stack

import (
	"testing"
	"time"
)

func TestConsumedExtraSurvivesTransferWithoutBatchHistory(t *testing.T) {
	f := noticeTestFixture(t)
	d := tasksFixture(t)
	f.send(t, f.a.Token, "@03-worker unread across transfer")

	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("first wake")
	}

	f.n.clock = func() time.Time { return time.Now().Add(11 * time.Minute) }
	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("extra wake")
	}

	snapshot, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/extra"})
	if err != nil {
		t.Fatal(err)
	}

	imported, err := d.service.Store.ImportTransfer(ctx, d.operator.Token, TransferImportRequest{Bundle: snapshot, AgentHashes: transferHashes(t, f.taskTeam, 0)})
	if err != nil {
		t.Fatal(err)
	}

	var consumed int
	if err = d.service.Store.db.QueryRow("SELECT extra_consumed FROM mail_mentions WHERE recipient_id=?", f.bID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}

	if consumed != 1 {
		t.Fatal("extra allowance reset")
	}

	if countTest(t, d.service.Store, "SELECT count(*) FROM notice_batches") != 0 {
		t.Fatal("batch history transferred")
	}

	_, err = d.service.Store.ChangeTransfer(ctx, d.operator.Token, "activate", TransferChangeRequest{Context: imported.Context, TransferID: snapshot.TransferID})
	if err != nil {
		t.Fatal(err)
	}

	var eligible int
	err = d.service.Store.db.QueryRow(`SELECT count(*) FROM mail_mentions m JOIN mail_messages msg ON msg.id=m.message_id JOIN agents a ON a.id=m.recipient_id WHERE a.id=? AND `+escalatableMentionSQL, f.bID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)).Scan(&eligible)
	if err != nil || eligible != 0 {
		t.Fatal("transfer granted another extra", eligible, err)
	}
}
