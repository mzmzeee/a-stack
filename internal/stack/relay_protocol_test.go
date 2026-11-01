package stack

import (
	"encoding/json"
	"testing"
)

func relayProtocolFixture(t *testing.T) (*noticeFixture, *Client, map[string]any) {
	t.Helper()
	f := noticeTestFixture(t)
	proof := opaqueID()
	binding := relayTestBinding(t, f.config.Binding, "protocol-machine")
	binding.RelayKeyHash = tokenHash(proof)
	credential := f.b
	credential.Token = opaqueID()

	_, err := f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 2, Runtime: &binding, CredentialHash: tokenHash(credential.Token)})
	if err != nil {
		t.Fatal(err)
	}

	client := clientForCredential(t, f.taskTeam, credential)
	f.send(t, f.a.Token, "@03-worker first private body")
	request := map[string]any{"workspace_id": f.ref(0).WorkspaceID, "session_id": f.ref(0).SessionID, "generation": 3, "runtime_id": binding.InstanceID, "proof": proof}

	return f, client, request
}

func TestRelayStatusWithoutServerAdapter(t *testing.T) {
	f, c, _ := relayProtocolFixture(t)
	data, err := c.Do(ctx, "POST", "/v1/notifications/status", NoticeRequest{Context: f.ref(0)})
	if err != nil {
		t.Fatal(err)
	}

	var reply struct {
		Data NoticeStatus `json:"data"`
	}
	if json.Unmarshal(data, &reply) != nil {
		t.Fatal("status JSON")
	}

	if len(reply.Data.Targets) != 1 || reply.Data.Targets[0].Reason == "integration_disabled" || !reply.Data.Enabled {
		t.Fatal("relay status hidden without server adapter")
	}
}

func TestRelayDeferredBackoff(t *testing.T) {
	f, c, q := relayProtocolFixture(t)
	claimRelayTest(t, c, q, false)

	q["outcome"], q["reason"] = "deferred", "blocked"
	if _, err := c.Do(ctx, "POST", "/v1/relay/result", q); err != nil {
		t.Fatal(err)
	}

	delete(q, "notice_id")
	delete(q, "lease_token")
	delete(q, "outcome")
	delete(q, "reason")

	data, err := c.Do(ctx, "POST", "/v1/relay/claim", q)
	if err != nil {
		t.Fatal(err)
	}

	var reply struct {
		Data RelayGrant `json:"data"`
	}
	if json.Unmarshal(data, &reply) != nil || reply.Data.NoticeID != "" {
		t.Fatal("defer bypassed backoff")
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM notice_batches") != 0 {
		t.Fatal("pre-begin defer created batch history")
	}
}

func TestRelayOneUseBeginAndCapturedResult(t *testing.T) {
	f, client, request := relayProtocolFixture(t)
	data, err := client.Do(ctx, "POST", "/v1/relay/claim", request)
	if err != nil {
		t.Fatal(err)
	}

	var reply struct {
		Data map[string]any `json:"data"`
	}
	if err = json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}

	if reply.Data["notice_id"] == nil || reply.Data["lease_token"] == nil {
		t.Fatal("no lease", string(data))
	}

	for key := range reply.Data {
		switch key {
		case "notice_id", "generation", "lease_token", "expires_at", "server_time":
		default:
			t.Fatal("unexpected grant field", key)
		}
	}

	request["notice_id"], request["lease_token"] = reply.Data["notice_id"], reply.Data["lease_token"]
	if _, err = client.Do(ctx, "POST", "/v1/relay/begin", request); err != nil {
		t.Fatal(err)
	}

	_, err = client.Do(ctx, "POST", "/v1/relay/begin", request)
	requireCode(t, err, "conflict")
	f.send(t, f.a.Token, "@03-worker later body")
	f.drain(t, 4)

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitting'") != 1 {
		t.Fatal("local drain recovered live relay batch")
	}

	request["outcome"] = "submitted"
	if _, err = client.Do(ctx, "POST", "/v1/relay/result", request); err != nil {
		t.Fatal(err)
	}

	if _, err = client.Do(ctx, "POST", "/v1/relay/result", request); err != nil {
		t.Fatal("exact result replay", err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'") != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 {
		t.Fatal("result consumed wrong references")
	}

	if f.prompts(t) != 0 {
		t.Fatal("server prompted remote pane")
	}
}
