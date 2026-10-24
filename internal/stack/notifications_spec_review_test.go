package stack

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Exercise one real external submission covering two Tasks while authority and
// pending mail change. The SQLite observations concern message IDs, not the
// dispatcher's internal batch-selection implementation.
func TestNoticeSpecMixedTaskSuppressionAndOutcomeRecovery(t *testing.T) {
	for _, failOutcome := range []bool{false, true} {
		name := "submitted"
		if failOutcome {
			name = "outcome-commit-failure-and-restart"
		}
		t.Run(name, func(t *testing.T) {
			f := noticeTestFixture(t)
			f.config.Mode = "prompt_wait"
			f.write(t)
			t.Cleanup(func() { f.release(t) })
			s := f.service.Store
			other := f.create(t, "Independent second mentioned Task")
			for i, agent := range []string{f.aID, f.bID} {
				f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: other.ResourceID, ExpectedRevision: int64(i + 1), AgentID: agent})
			}
			first := f.send(t, f.a.Token, "First Task private body @03-worker")
			second, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: other.ResourceID, OperationID: opaqueID(), Body: "Second Task private body @03-worker"})
			if err != nil {
				t.Fatal(err)
			}
			type outcome struct {
				result NoticeDrainResult
				err    error
			}
			finished := make(chan outcome, 1)
			go func() {
				r, e := f.n.drain(ctx, "", 1)
				finished <- outcome{r, e}
			}()
			f.wait(t, "prompt-started")
			// These actual writes must finish while the fake external process is
			// still blocked. A held SQLite connection/transaction times out here.
			writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, err = s.ChangeTask(writeCtx, f.orch.Token, "unassign", TaskChangeRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), ExpectedRevision: 3, AgentID: f.bID})
			if err != nil {
				t.Fatal("in-flight removal blocked or failed", err)
			}
			_, err = s.ChangeTask(writeCtx, f.orch.Token, "assign", TaskChangeRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), ExpectedRevision: 4, AgentID: f.bID})
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.ChangeTask(writeCtx, f.a.Token, "attention.set", TaskChangeRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: opaqueID(), ExpectedRevision: 5, Question: "Preserve this independent Operator question"})
			if err != nil {
				t.Fatal(err)
			}
			arrival, err := s.SendMail(writeCtx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: other.ResourceID, OperationID: opaqueID(), Body: "Arrival during submission @03-worker"})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]NoticeState{first.ResourceID: NoticeSuppressed, second.ResourceID: NoticeSubmitting, arrival.ResourceID: NoticePending}
			assertStates := func() {
				t.Helper()
				for id, expected := range want {
					var state NoticeState
					if err := s.db.QueryRow("SELECT notice_state FROM mail_mentions WHERE message_id=? AND recipient_id=?", id, f.bID).Scan(&state); err != nil || state != expected {
						t.Fatalf("reference state=%q, want=%q: %v", state, expected, err)
					}
				}
			}
			assertStates()
			if countTest(t, s, "SELECT count(*) FROM notice_batch_refs") != 2 {
				t.Fatal("new arrival joined already captured batch")
			}
			if failOutcome {
				execTest(t, s, `CREATE TRIGGER review_fail_notice_outcome BEFORE UPDATE OF state ON notice_batches WHEN NEW.state='submitted' BEGIN SELECT RAISE(ABORT,'independent outcome failure'); END`)
			}
			f.release(t)
			result := <-finished
			if failOutcome {
				if result.err == nil {
					t.Fatal("failed outcome commit reported success")
				}
				assertStates()
				execTest(t, s, "DROP TRIGGER review_fail_notice_outcome")
				stateDir := f.service.lock.dir
				if err := f.service.Close(ctx); err != nil {
					t.Fatal(err)
				}
				restarted, _ := reviewService(t, stateDir)
				s = restarted.Store
				f.n.store = s
				want[second.ResourceID] = NoticeUncertain
			} else {
				if result.err != nil || result.result.Submitted != 1 {
					t.Fatal("submission failed", result.err)
				}
				want[second.ResourceID] = NoticeSubmitted
			}
			assertStates()
			if f.prompts(t) != 1 {
				t.Fatal("completion/recovery repeated covered input")
			}
			f.config.Mode = ""
			f.write(t)
			if f.drain(t, 1).Submitted != 1 || f.prompts(t) != 2 {
				t.Fatal("new pending arrival did not form one fresh submission")
			}
			want[arrival.ResourceID] = NoticeSubmitted
			assertStates()
			if countTest(t, s, "SELECT count(*) FROM notice_batch_refs") != 3 || countTest(t, s, "SELECT count(*) FROM mail_messages") != 3 {
				t.Fatal("captured associations or shared bodies duplicated/lost")
			}
			detail, err := s.ShowTask(ctx, f.a.Token, TaskReadRequest{Context: f.ref(0), TaskID: f.taskID})
			if err != nil || !detail.NeedsYou || detail.Progress != "waiting" {
				t.Fatal("dispatch altered Task progress/Operator attention")
			}
			page, err := s.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
			if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != first.ResourceID {
				t.Fatal("suppression/rejoin erased retained shared mail")
			}
		})
	}
}

func TestNoticeSpecProductionGateLeavesUnknownHarnessMailOnly(t *testing.T) {
	f := noticeTestFixture(t)
	f.send(t, f.a.Token, "Retained with unsupported harness @03-worker")
	binding := f.config.Binding
	binding.Harness = "unsupported-harness"
	_, err := f.service.Store.BindAgent(ctx, f.operator.Token, "rebind", AgentBindingRequest{Context: f.ref(0), AgentID: f.bID, OperationID: opaqueID(), ExpectedRevision: 2, Runtime: &binding, CredentialHash: tokenHash(opaqueID())})
	if err != nil {
		t.Fatal(err)
	}
	// Use the real production support predicate and only a fake executable.
	n := newNotifier(f.service.Store, newHerdr(filepath.Join(f.dir, "herdr")))
	result, err := n.drain(ctx, f.ref(0).SessionID, 1)
	if err != nil || result.Pending != 1 || result.Submitted != 0 || len(f.calls(t)) != 0 {
		t.Fatal("unknown harness invoked external input/metadata", err)
	}
	var reason string
	if err := f.service.Store.db.QueryRow("SELECT reason FROM notice_targets WHERE recipient_id=?", f.bID).Scan(&reason); err != nil || reason != "unsupported_harness" {
		t.Fatal("unknown support diagnostic lost", err)
	}
	if countTest(t, f.service.Store, "SELECT count(*) FROM mail_messages") != 1 || countTest(t, f.service.Store, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'") != 1 {
		t.Fatal("support gate discarded shared mail or pending reference")
	}
}
