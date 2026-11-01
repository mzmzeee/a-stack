package stack

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRelayExpirySweepBound(t *testing.T) {
	f, _, _ := relayProtocolFixture(t)
	for i := range 101 {
		agent, _ := lifecycleReviewAgent(t, f.service.Store, f.operator, f.ref(0), fmt.Sprintf("sweep-%d", i))
		_, err := f.service.Store.db.Exec("INSERT INTO notice_relay_leases VALUES(?,?,?,?,?,?,?,?)", f.ref(0).SessionID, agent.ResourceID, opaqueID(), 1, "old-binding", "old-lease", 1, "old-instance")
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, remaining := range []int{1, 0} {
		n, err := f.service.Store.expireRelayLeases(ctx, time.Now())
		if err != nil || n > 100 {
			t.Fatal("unbounded expiry sweep", n, err)
		}

		if countTest(t, f.service.Store, "SELECT count(*) FROM notice_relay_leases") != remaining {
			t.Fatal("expiry bound")
		}
	}
}

func TestRelayAdmissionRefusesBeforeBody(t *testing.T) {
	f := newMatrixFixture(t)
	f.service.relayInFlight.Store(4)

	for _, action := range []string{"claim", "begin", "result"} {
		code, reads := f.call(t, "/v1/relay/"+action, "worker")
		if code != "rate_limited" || reads != 0 {
			t.Fatalf("admission: %s reads %d", code, reads)
		}
	}

	if f.service.relayInFlight.Load() != 4 {
		t.Fatal("admission leaked")
	}

	f.service.relayInFlight.Store(0)

	if code, _ := f.call(t, "/v1/relay/claim", "worker"); code != "allowed" {
		t.Fatal(code)
	}

	if f.service.relayInFlight.Load() != 0 {
		t.Fatal("success leaked admission")
	}
}

func TestRelayRejectsProofGenerationAndContextBeforeMutation(t *testing.T) {
	f, c, q := relayProtocolFixture(t)
	for _, action := range []string{"claim", "begin", "result"} {
		for _, field := range []string{"proof", "generation", "runtime_id", "workspace_id", "session_id"} {
			t.Run(action+"/"+field, func(t *testing.T) {
				bad := map[string]any{}
				for k, v := range q {
					bad[k] = v
				}

				bad[field] = opaqueID()
				if field == "generation" {
					bad[field] = 2
				}

				_, err := c.Do(ctx, "POST", "/v1/relay/"+action, bad)
				var api *Error
				if !errors.As(err, &api) || api.Code != "forbidden" {
					t.Fatalf("wanted forbidden, got %v", err)
				}
			})
		}
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM notice_relay_leases") != 0 || countTest(t, f.service.Store, "SELECT count(*) FROM notice_batches") != 0 {
		t.Fatal("invalid request wrote delivery state")
	}
}

func TestRelayCapturedSuppressionWinsAfterRejoin(t *testing.T) {
	for _, outcome := range []string{"submitted", "uncertain"} {
		t.Run(outcome, func(t *testing.T) {
			f, c, q := relayProtocolFixture(t)
			claimRelayTest(t, c, q, true)
			f.change(t, f.orch.Token, "unassign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.bID})
			f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: f.bID})
			f.send(t, f.a.Token, "@03-worker new after rejoin")
			q["outcome"] = outcome

			_, err := c.Do(ctx, "POST", "/v1/relay/result", q)
			if err != nil {
				var api *Error
				if !errors.As(err, &api) || api.Code != "conflict" {
					t.Fatal(err)
				}
			}

			if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='suppressed'") != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 {
				t.Fatal("old result overwrote suppression or later mail")
			}
		})
	}
}

func TestRelayCaptureBoundAndSingleReservation(t *testing.T) {
	f, c, q := relayProtocolFixture(t)
	for range 500 {
		f.send(t, f.a.Token, "@03-worker bounded")
	}

	claimRelayTest(t, c, q, false)

	identity := map[string]any{}
	for k, v := range q {
		if k != "notice_id" && k != "lease_token" {
			identity[k] = v
		}
	}

	data, err := c.Do(ctx, "POST", "/v1/relay/claim", identity)
	if err != nil {
		t.Fatal(err)
	}

	var reply struct {
		Data RelayGrant `json:"data"`
	}
	if json.Unmarshal(data, &reply) != nil || reply.Data.NoticeID != "" {
		t.Fatal("second relay reserved same Agent")
	}

	if _, err = c.Do(ctx, "POST", "/v1/relay/begin", q); err != nil {
		t.Fatal(err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM notice_batch_refs") != 500 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 {
		t.Fatal("capture exceeded bound or lost overflow")
	}

	q["outcome"] = "submitted"
	if _, err = c.Do(ctx, "POST", "/v1/relay/result", q); err != nil {
		t.Fatal(err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'") != 500 {
		t.Fatal("result changed uncaptured refs")
	}
}
