package stack

import (
	"testing"
	"time"
)

// Transfer does not carry batch history. The portable budget must survive its loss.
func TestExtraBudgetDoesNotDependOnBatchHistory(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "Question @03-worker")

	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("first notice missing")
	}

	clock = clock.Add(11 * time.Minute)

	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("extra missing")
	}

	execTest(t, f.service.Store, "DELETE FROM notice_batch_refs")
	execTest(t, f.service.Store, "DELETE FROM notice_batches")
	clock = clock.Add(11 * time.Minute)

	if got := f.drain(t, 4).Submitted; got != 0 {
		t.Fatalf("lost history granted %d extra notices", got)
	}
}

func TestLocalPaneDeadlineSurvivesClearAndFreshBind(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "First @03-worker")

	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("first notice missing")
	}

	credential := opaqueID()

	r, err := f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: 2, CredentialHash: tokenHash(credential)})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.service.Store.BindAgent(ctx, credential, "attach", AgentBindingRequest{Context: f.ref(0), OperationID: opaqueID(), AgentID: f.bID, ExpectedRevision: r.Revision, Runtime: &f.config.Binding})
	if err != nil {
		t.Fatal(err)
	}

	f.send(t, f.a.Token, "Later @03-worker")

	if got := f.drain(t, 4).Submitted; got != 0 {
		t.Fatalf("rebind erased cooldown: %d prompts", got)
	}

	clock = clock.Add(11 * time.Minute)

	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("deadline never released")
	}
}
