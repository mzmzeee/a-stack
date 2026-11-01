package stack

import (
	"os"
	"testing"
	"time"
)

func TestExtraNoticeRetriesOnlyDefinitePreinputFailure(t *testing.T) {
	for _, mode := range []string{"blocked", "remove_executable", "error", "bad_success"} {
		t.Run(mode, func(t *testing.T) {
			f := noticeTestFixture(t)
			clock := time.Now()
			f.n.clock = func() time.Time { return clock }
			f.send(t, f.a.Token, "Question @03-worker")

			if f.drain(t, 4).Submitted != 1 {
				t.Fatal("initial notice missing")
			}

			executable, err := os.ReadFile(f.n.adapter.executable)
			if err != nil {
				t.Fatal(err)
			}

			clock = clock.Add(11 * time.Minute)
			f.config.Mode = mode
			f.write(t)

			result := f.drain(t, 1)
			retry := mode == "blocked" || mode == "remove_executable"
			if retry && result.Pending != 1 || !retry && result.Uncertain != 1 {
				t.Fatalf("unexpected extra outcome: %+v", result)
			}

			consumed := countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE extra_consumed=1")
			if retry && consumed != 0 || !retry && consumed != 1 {
				t.Fatalf("consumed extra after %s: %d", mode, consumed)
			}

			f.config.Mode = ""
			f.write(t)

			if mode == "remove_executable" {
				if err = os.WriteFile(f.n.adapter.executable, executable, 0o700); err != nil {
					t.Fatal(err)
				}
			}

			clock = clock.Add(11 * time.Minute)

			want := 0
			if retry {
				want = 1
			}

			if got := f.drain(t, 4).Submitted; got != want {
				t.Fatalf("retried extra submissions=%d want=%d", got, want)
			}

			clock = clock.Add(11 * time.Minute)

			if f.drain(t, 4).Submitted != 0 {
				t.Fatal("second extra was offered")
			}

			if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE extra_consumed=1") != 1 {
				t.Fatal("terminal extra lost consumed marker")
			}
		})
	}
}

func TestExtraNoticeRecoveryKeepsConsumedBudget(t *testing.T) {
	f := noticeTestFixture(t)
	clock := time.Now()
	f.n.clock = func() time.Time { return clock }
	f.send(t, f.a.Token, "Question @03-worker")

	if f.drain(t, 4).Submitted != 1 {
		t.Fatal("initial notice missing")
	}

	clock = clock.Add(11 * time.Minute)

	target, err := f.n.nextTarget(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	batch, err := f.n.claim(ctx, target)
	if err != nil || !batch.extra {
		t.Fatal("extra claim missing", err)
	}

	state, _, err := f.n.preflight(ctx, batch)
	if err != nil || state != "" {
		t.Fatal("extra preflight failed", err)
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE extra_consumed=1") != 1 {
		t.Fatal("extra not consumed before external input")
	}

	if err = f.service.Store.recoverNotices(ctx); err != nil {
		t.Fatal(err)
	}

	clock = clock.Add(11 * time.Minute)

	if f.drain(t, 4).Submitted != 0 || f.prompts(t) != 1 {
		t.Fatal("recovered ambiguous extra was submitted")
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE extra_consumed=1 AND notice_state='uncertain'") != 1 {
		t.Fatal("recovery restored extra allowance")
	}
}

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
