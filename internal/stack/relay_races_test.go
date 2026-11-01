package stack

import (
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func relayRequestTest(t *testing.T, q map[string]any) RelayRequest {
	t.Helper()

	b, e := json.Marshal(q)
	if e != nil {
		t.Fatal(e)
	}

	var r RelayRequest
	if e = json.Unmarshal(b, &r); e != nil {
		t.Fatal(e)
	}

	return r
}

func TestRelayConcurrentClaimsAndBegins(t *testing.T) {
	f, c, q := relayProtocolFixture(t)
	request := relayRequestTest(t, q)
	var wg sync.WaitGroup
	start := make(chan struct{})

	grants := make(chan RelayGrant, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start

			v, e := f.service.Store.relay(ctx, c.Credential.Token, "claim", f.service.Discovery.InstanceID, request, time.Now())
			if e != nil {
				errs <- e
				return
			}

			g := v.(RelayGrant)
			if g.NoticeID != "" {
				grants <- g
			}
		}()
	}

	close(start)
	wg.Wait()
	close(grants)
	close(errs)

	for e := range errs {
		t.Error(e)
	}

	if len(grants) != 1 {
		t.Fatalf("wanted one claim got %d", len(grants))
	}

	g := <-grants
	request.NoticeID = g.NoticeID
	request.LeaseToken = g.LeaseToken

	start = make(chan struct{})
	success := make(chan bool, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := f.service.Store.relay(ctx, c.Credential.Token, "begin", f.service.Discovery.InstanceID, request, time.Now())
			success <- e == nil
		}()
	}

	close(start)
	wg.Wait()
	close(success)

	n := 0
	for ok := range success {
		if ok {
			n++
		}
	}

	if n != 1 {
		t.Fatalf("wanted one begin grant got %d", n)
	}
}

func TestRelayOldResultCannotTouchNewBinding(t *testing.T) {
	f, c, q := relayProtocolFixture(t)
	claimRelayTest(t, c, q, true)
	old := relayRequestTest(t, q)
	old.Outcome = "submitted"
	start, ready, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		close(ready)
		<-start
		_, err := f.service.Store.relay(ctx, c.Credential.Token, "result", f.service.Discovery.InstanceID, old, time.Now())
		done <- err
	}()
	<-ready

	next := relayTestBinding(t, f.config.Binding, "new-machine")
	credential := opaqueID()
	if _, err := f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 3, Runtime: &next, CredentialHash: tokenHash(credential)}); err != nil {
		t.Fatal(err)
	}

	// A newer diagnostic must survive an old suspended process resuming.
	execTest(t, f.service.Store, "INSERT INTO notice_targets(session_id,recipient_id,reason,failures,next_attempt,updated_at) VALUES(?,?,'new_binding_diagnostic',0,0,'now') ON CONFLICT(session_id,recipient_id) DO UPDATE SET reason='new_binding_diagnostic'", f.sessions[0], f.bID)
	close(start)

	if err := <-done; err == nil {
		t.Fatal("old report accepted")
	}

	var reason string
	if err := f.service.Store.db.QueryRow("SELECT reason FROM notice_targets WHERE recipient_id=?", f.bID).Scan(&reason); err != nil || reason != "new_binding_diagnostic" {
		t.Fatalf("new diagnostic overwritten: %s %v", reason, err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='uncertain'") != 1 {
		t.Fatal("old references lost uncertainty")
	}
}

func TestRelayBeginSealBarrierOrders(t *testing.T) {
	for _, order := range []string{"seal-first", "begin-first"} {
		t.Run(order, func(t *testing.T) {
			f, c, q := relayProtocolFixture(t)
			claimRelayTest(t, c, q, false)

			request := relayRequestTest(t, q)
			bundle, err := f.service.Store.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/barrier"})
			if err != nil {
				t.Fatal(err)
			}

			ready, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			go func() {
				close(ready)
				<-release
				_, e := f.service.Store.relay(ctx, c.Credential.Token, "begin", f.service.Discovery.InstanceID, request, time.Now())
				done <- e
			}()
			<-ready

			seal := func() error {
				_, e := f.service.Store.ChangeTransfer(ctx, f.operator.Token, "seal", TransferChangeRequest{Context: f.ref(0), TransferID: bundle.TransferID, DestinationDatabase: opaqueID()})
				return e
			}
			if order == "seal-first" {
				err = seal()
				close(release)

				beginErr := <-done
				if err != nil || beginErr == nil {
					t.Fatalf("seal=%v begin=%v", err, beginErr)
				}
			} else {
				close(release)

				if e := <-done; e != nil {
					t.Fatal(e)
				}

				if e := seal(); e == nil {
					t.Fatal("seal ignored new begin state")
				}
			}
		})
	}
}

func TestRelayExpiryWinsAgainstDelayedResult(t *testing.T) {
	f, c, q := relayProtocolFixture(t)
	claimRelayTest(t, c, q, true)
	request := relayRequestTest(t, q)
	request.Outcome = "submitted"
	ready, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		close(ready)
		<-release
		_, e := f.service.Store.relay(ctx, c.Credential.Token, "result", f.service.Discovery.InstanceID, request, time.Now().Add(7*24*time.Hour))
		done <- e
	}()
	<-ready

	if _, err := f.service.Store.expireRelayLeases(ctx, time.Now().Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	close(release)

	if err := <-done; err == nil {
		t.Fatal("expired report accepted")
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM notice_relay_leases") != 0 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='uncertain'") != 1 {
		t.Fatal("offline expiry did not retire exact work")
	}
}
