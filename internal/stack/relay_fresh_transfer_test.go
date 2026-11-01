package stack

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRelayTransferFreshAttachAndPortableBudget(t *testing.T) {
	f, c, q := relayProtocolFixture(t)
	d := tasksFixture(t)
	base := time.Now()
	identity := relayRequestTest(t, q)
	deliver := func(s *Store, token, instance string, r RelayRequest, at time.Time) {
		t.Helper()

		v, err := s.relay(ctx, token, "claim", instance, r, at)
		if err != nil {
			t.Fatal(err)
		}

		g := v.(RelayGrant)
		if g.NoticeID == "" {
			t.Fatal("expected eligible notice")
		}

		r.NoticeID, r.LeaseToken = g.NoticeID, g.LeaseToken
		if _, err = s.relay(ctx, token, "begin", instance, r, at); err != nil {
			t.Fatal(err)
		}

		r.Outcome = "submitted"
		if _, err = s.relay(ctx, token, "result", instance, r, at); err != nil {
			t.Fatal(err)
		}
	}
	deliver(f.service.Store, c.Credential.Token, f.service.Discovery.InstanceID, identity, base)
	deliver(f.service.Store, c.Credential.Token, f.service.Discovery.InstanceID, identity, base.Add(12*time.Minute))

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE extra_consumed=1") != 1 {
		t.Fatal("extra not consumed")
	}

	f.send(t, f.a.Token, "@03-worker fresh after extra")

	b, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/reviewer"})
	if err != nil {
		t.Fatal(err)
	}

	hashes := transferHashes(t, f.taskTeam, 0)
	destToken := opaqueID()
	hashes[f.bID] = tokenHash(destToken)

	imported, err := d.service.Store.ImportTransfer(ctx, d.operator.Token, TransferImportRequest{Bundle: b, AgentHashes: hashes})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = f.service.Store.ChangeTransfer(ctx, f.operator.Token, "seal", TransferChangeRequest{Context: f.ref(0), TransferID: b.TransferID, DestinationDatabase: d.service.Store.id}); err != nil {
		t.Fatal(err)
	}

	if _, err = d.service.Store.ChangeTransfer(ctx, d.operator.Token, "activate", TransferChangeRequest{Context: imported.Context, TransferID: b.TransferID}); err != nil {
		t.Fatal(err)
	}

	proof := opaqueID()
	binding := relayTestBinding(t, f.config.Binding, "destination-machine")
	binding.InstanceID = "herdr:" + binding.TerminalID
	binding.RelayKeyHash = tokenHash(proof)
	credential := Credential{Kind: AgentCapability, Token: destToken}

	dc := clientForCredential(t, d, credential)
	data, err := dc.Do(ctx, "POST", "/v1/herdr/bind", AgentBindingRequest{Context: imported.Context, OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 4, Runtime: &binding})
	if err != nil {
		t.Fatal("fresh attach after transfer", err)
	}

	var receipt struct{ Data Receipt }
	if err = json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}

	destQ := RelayRequest{Context: imported.Context, Generation: receipt.Data.Revision, RuntimeID: binding.InstanceID, Proof: proof}

	value, err := d.service.Store.relay(ctx, destToken, "claim", d.service.Discovery.InstanceID, destQ, base.Add(13*time.Minute))
	if err != nil || value.(RelayGrant).NoticeID != "" {
		t.Fatal("transfer bypassed pane cooldown", err)
	}

	deliver(d.service.Store, destToken, d.service.Discovery.InstanceID, destQ, base.Add(30*time.Minute))

	if countTest(t, d.service.Store, "SELECT count(*) FROM notice_batch_refs") != 1 {
		t.Fatal("old consumed mention joined new destination batch")
	}

	if countTest(t, d.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted' AND extra_consumed=1") != 1 {
		t.Fatal("portable consumed budget changed")
	}
}
