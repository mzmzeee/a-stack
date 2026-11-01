package stack

import (
	"encoding/json"
	"testing"
)

func relayTestBinding(t *testing.T, base RuntimeBinding, machine string) RuntimeBinding {
	t.Helper()
	data, _ := json.Marshal(base)
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	fields["relay_machine"], fields["relay_key_hash"] = tokenHash(machine), tokenHash("relay-test-proof")
	fields["remote_machine"] = true
	data, _ = json.Marshal(fields)
	if err := json.Unmarshal(data, &base); err != nil {
		t.Fatal(err)
	}
	return base
}

func TestRelayBindingMachineNamespacesAndNoServerProbe(t *testing.T) {
	for _, adapter := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_adapter", true: "with_adapter"}[adapter], func(t *testing.T) {
			f := noticeTestFixture(t)
			store := f.service.Store
			var h *herdrAdapter
			if adapter {
				h = f.n.adapter
			}
			for i, machine := range []string{"machine-a", "machine-b", "machine-a"} {
				names := []string{"relay-a", "relay-b", "relay-duplicate"}
				agent, cred := lifecycleReviewAgent(t, store, f.operator, f.ref(0), names[i])
				binding := relayTestBinding(t, f.config.Binding, machine)
				binding.InstanceID = "herdr:" + binding.TerminalID
				// There is already a legacy local target with the identical concrete IDs.
				_, err := store.bindHerdrAgent(ctx, cred.Token, AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: agent.ResourceID, ExpectedRevision: 1, Runtime: &binding}, h)
				if i == 2 {
					requireCode(t, err, "conflict")
				} else if err != nil {
					t.Fatal("independent machine refused", err)
				}
				if i == 0 && err == nil {
					_, err = store.bindHerdrAgent(ctx, cred.Token, AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: agent.ResourceID, ExpectedRevision: 2, Runtime: &binding}, h)
					requireCode(t, err, "conflict")
				}
			}
			if len(f.calls(t)) != 0 {
				t.Fatal("server probed remote socket")
			}
		})
	}
}
