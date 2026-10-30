package stack

import (
	"encoding/json"
	"testing"
)

func TestTransferKeepsOtherMachineRemote(t *testing.T) {
	source, dest := tasksFixture(t), tasksFixture(t)
	binding, _ := json.Marshal(RuntimeBinding{Machine: "other-laptop", RemoteMachine: true})
	if _, err := source.service.Store.db.Exec("UPDATE agents SET binding=? WHERE id=?", string(binding), source.aID); err != nil {
		t.Fatal(err)
	}

	b, err := source.service.Store.ExportTransfer(ctx, source.operator.Token, TransferExportRequest{Context: source.ref(0), TransferID: opaqueID(), Repository: "example.com/machines"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = dest.service.Store.ImportTransfer(ctx, dest.operator.Token, TransferImportRequest{Bundle: b, AgentHashes: transferHashes(t, source, 0), Machine: "this-laptop", LocalRuntime: true}); err != nil {
		t.Fatal(err)
	}

	var raw string
	if err = dest.service.Store.db.QueryRow("SELECT binding FROM agents WHERE id=?", source.aID).Scan(&raw); err != nil {
		t.Fatal(err)
	}

	var got RuntimeBinding
	if json.Unmarshal([]byte(raw), &got) != nil || got.Machine != "other-laptop" || !got.RemoteMachine {
		t.Fatal("remote binding was reassigned to the importing machine")
	}
}
