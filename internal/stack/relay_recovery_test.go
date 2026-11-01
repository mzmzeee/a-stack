package stack

import (
	"encoding/json"
	"testing"
	"time"
)

func claimRelayTest(t *testing.T, c *Client, q map[string]any, begin bool) {
	t.Helper()

	data, err := c.Do(ctx, "POST", "/v1/relay/claim", q)
	if err != nil {
		t.Fatal(err)
	}

	var reply struct {
		Data RelayGrant `json:"data"`
	}
	if json.Unmarshal(data, &reply) != nil || reply.Data.NoticeID == "" {
		t.Fatal("missing lease")
	}

	q["notice_id"], q["lease_token"] = reply.Data.NoticeID, reply.Data.LeaseToken
	if begin {
		if _, err = c.Do(ctx, "POST", "/v1/relay/begin", q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRelayExpiryAndRebindRetireBegunWork(t *testing.T) {
	for _, action := range []string{"expiry", "rebind", "seal"} {
		t.Run(action, func(t *testing.T) {
			f, c, q := relayProtocolFixture(t)
			claimRelayTest(t, c, q, true)

			s := f.service.Store
			if countTest(t, s, "SELECT count(*) FROM agents a JOIN notice_relay_leases l ON l.recipient_id=a.id WHERE a.pane_not_before>=l.expires_at+600000") != 1 {
				t.Fatal("begin omitted durable expiry-plus-cooldown hold")
			}

			if action == "expiry" {
				if _, err := s.expireRelayLeases(ctx, clockNow().Add(2*time.Minute)); err != nil {
					t.Fatal(err)
				}
			} else if action == "seal" {
				b, err := s.ExportTransfer(ctx, f.operator.Token, TransferExportRequest{Context: f.ref(0), TransferID: opaqueID(), Repository: "example.com/relay"})
				if err != nil {
					t.Fatal(err)
				}

				if _, err = s.ChangeTransfer(ctx, f.operator.Token, "seal", TransferChangeRequest{Context: f.ref(0), TransferID: b.TransferID, DestinationDatabase: opaqueID()}); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := s.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 3, CredentialHash: tokenHash(opaqueID())}); err != nil {
					t.Fatal(err)
				}
			}

			if countTest(t, s, "SELECT count(*) FROM notice_relay_leases") != 0 || countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='uncertain'") != 1 {
				t.Fatal("old grant was not retired")
			}

			q["outcome"] = "submitted"
			if _, err := c.Do(ctx, "POST", "/v1/relay/result", q); err == nil {
				t.Fatal("late result accepted")
			}
		})
	}
}

func TestRelayStartupRetiresClaimsAndOrphans(t *testing.T) {
	for _, kind := range []string{"claim", "begun", "orphan", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			f, c, q := relayProtocolFixture(t)
			claimRelayTest(t, c, q, kind != "claim")

			if kind == "terminal" {
				q["outcome"] = "submitted"
				if _, err := c.Do(ctx, "POST", "/v1/relay/result", q); err != nil {
					t.Fatal(err)
				}
			}

			if kind == "orphan" {
				execTest(t, f.service.Store, "DELETE FROM notice_relay_leases")
			}

			var hold int64
			if err := f.service.Store.db.QueryRow("SELECT pane_not_before FROM agents WHERE id=?", f.bID).Scan(&hold); err != nil {
				t.Fatal(err)
			}

			f.service.Close(ctx)

			restored, err := Start(ctx, f.taskTeam.dir, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close(ctx)

			s := restored.Store
			if countTest(t, s, "SELECT count(*) FROM notice_relay_leases") != 0 {
				t.Fatal("startup retained old lease")
			}

			want := "uncertain"
			if kind == "claim" {
				want = "pending"
			}

			if kind == "terminal" {
				want = "submitted"
			}

			var state string
			if err = s.db.QueryRow("SELECT notice_state FROM mail_mentions WHERE recipient_id=?", f.bID).Scan(&state); err != nil || state != want {
				t.Fatalf("state=%s want=%s error=%v", state, want, err)
			}

			var after int64
			if err = s.db.QueryRow("SELECT pane_not_before FROM agents WHERE id=?", f.bID).Scan(&after); err != nil || after != hold {
				t.Fatal("startup changed durable hold", err)
			}
		})
	}
}
