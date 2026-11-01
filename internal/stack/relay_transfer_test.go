package stack

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTransferDropsRoutesButPreservesPaneBudget(t *testing.T) {
	f := noticeTestFixture(t)
	d := tasksFixture(t)
	f.send(t, f.a.Token, "@03-worker portable budget")
	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("initial delivery")
	}
	snapshot, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/relay"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(snapshot)
	for _, secret := range []string{f.config.Binding.InstanceID, f.config.Binding.TerminalID, f.config.Binding.NativeSessionID} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("portable snapshot contains route %q", secret)
		}
	}
	imported, err := d.service.Store.ImportTransfer(ctx, d.operator.Token, TransferImportRequest{Bundle: snapshot, AgentHashes: transferHashes(t, f.taskTeam, 0)})
	if err != nil {
		t.Fatal(err)
	}
	var attached bool
	var hold, revision int64
	if err := d.service.Store.db.QueryRow("SELECT runtime_id IS NOT NULL,pane_not_before,revision FROM agents WHERE id=?", f.bID).Scan(&attached, &hold, &revision); err != nil {
		t.Fatal(err)
	}
	if attached || hold == 0 || revision != 3 {
		t.Fatal("import did not clear attachment and preserve hold", attached, hold, revision)
	}
	if imported.State != "staged" {
		t.Fatal(imported)
	}
}

func TestTransferSealFencesRelayLeaseAndTargetHold(t *testing.T) {
	for _, change := range []string{"lease", "target", "binding"} {
		t.Run(change, func(t *testing.T) {
			f := noticeTestFixture(t)
			s := f.service.Store
			b, err := s.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/relay"})
			if err != nil {
				t.Fatal(err)
			}
			if change == "lease" {
				execTest(t, s, "INSERT INTO notice_relay_leases VALUES(?,?,?,?,?,?,?,?)", f.sessions[0], f.bID, opaqueID(), 2, tokenHash("binding"), tokenHash("lease"), int64(9999999999999), opaqueID())
			} else if change == "binding" {
				execTest(t, s, "UPDATE agents SET binding='{}' WHERE id=?", f.bID)
			} else {
				execTest(t, s, "INSERT INTO notice_targets(session_id,recipient_id,failures,reason,next_attempt,updated_at) VALUES(?,?,0,'working',123,'now')", f.sessions[0], f.bID)
			}
			_, err = s.ChangeTransfer(ctx, f.operator.Token, "seal", TransferChangeRequest{Context: f.ref(0), TransferID: b.TransferID, DestinationDatabase: opaqueID()})
			requireCode(t, err, "conflict")
		})
	}
}
