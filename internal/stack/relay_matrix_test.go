package stack

import (
	"path/filepath"
	"testing"
)

type matrixRelay struct {
	token   string
	request RelayRequest
}

func (f *matrixFixture) prepareRelays(t *testing.T) {
	t.Helper()

	f.relays = map[string]matrixRelay{}
	for _, action := range []string{"claim", "begin", "result"} {
		name := "relay-" + action
		credential, registered := f.register(t, 0, name)
		id := registered.ResourceID

		proof := opaqueID()
		b := RuntimeBinding{InstanceID: name, HerdrAlias: name, Endpoint: filepath.Join(f.path, "relay.sock"), PaneID: name, TerminalID: name, NativeSessionID: name, Harness: "codex", HarnessVersion: "0.164.0", Notices: "pane", RelayMachine: tokenHash("matrix-machine"), RelayKeyHash: tokenHash(proof)}
		if _, err := f.service.Store.BindAgent(ctx, credential.Token, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: id, ExpectedRevision: 1, Runtime: &b}); err != nil {
			t.Fatal(err)
		}

		f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.task, ExpectedRevision: f.show(t, f.task).Revision, AgentID: id})

		if _, err := f.service.Store.SendMail(ctx, f.orch.Token, MailSendRequest{Context: f.ref(0), OperationID: opaqueID(), TaskID: f.task, Body: "@" + name + " matrix"}); err != nil {
			t.Fatal(err)
		}

		q := RelayRequest{Context: f.ref(0), Generation: 2, RuntimeID: b.InstanceID, Proof: proof}
		if action != "claim" {
			result, err := f.service.Store.relay(ctx, credential.Token, "claim", f.service.Discovery.InstanceID, q, clockNow())
			if err != nil {
				t.Fatal(err)
			}

			grant := result.(RelayGrant)
			if grant.NoticeID == "" {
				t.Fatal("matrix lease missing")
			}

			q.NoticeID, q.LeaseToken = grant.NoticeID, grant.LeaseToken
			if action == "result" {
				if _, err = f.service.Store.relay(ctx, credential.Token, "begin", f.service.Discovery.InstanceID, q, clockNow()); err != nil {
					t.Fatal(err)
				}

				q.Outcome = "submitted"
			}
		}

		f.relays["/v1/relay/"+action] = matrixRelay{credential.Token, q}
	}
}

func TestRelayProtocolStateMatrix(t *testing.T) {
	f := newMatrixFixture(t)
	for _, action := range []string{"claim", "begin", "result"} {
		for _, session := range matrixSessionStates {
			for _, task := range matrixTaskStates {
				t.Run(action+"/"+session+"/"+task, func(t *testing.T) {
					f.reset(t)
					f.state(t, session, task)

					want := "allowed"
					switch session {
					case "closed":
						if action != "result" {
							want = "session_closed"
						}
					case "sealed", "staged":
						want = "conflict"
					}

					for _, caller := range matrixCallers {
						f.reset(t)
						f.state(t, session, task)

						expected := want
						if caller != "worker" {
							expected = "forbidden"
						}

						if caller == "none" || caller == "bad" || caller == "revoked" {
							expected = "unauthorized"
						}

						if got, _ := f.call(t, "/v1/relay/"+action, caller); got != expected {
							t.Errorf("%s: want %s got %s", caller, expected, got)
						}
					}
				})
			}
		}
	}
}
