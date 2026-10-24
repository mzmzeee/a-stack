package stack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestNotificationsLatencyBatchingAndGrowth(t *testing.T) {
	f := noticeTestFixture(t)
	s := f.service.Store
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.orchID})
	c, third := f.identityFixture.register(t, 0, "04-worker")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: third.ResourceID})
	for i, item := range []struct{ id, token, alias string }{{f.orchID, f.orch.Token, "fixture-orch"}, {third.ResourceID, c.Token, "fixture-c"}} {
		binding := f.config.Binding
		binding.InstanceID = fmt.Sprintf("measure-%d", i)
		binding.HerdrAlias = item.alias
		binding.PaneID = fmt.Sprintf("w1:p%d", i+3)
		binding.TerminalID = fmt.Sprintf("terminal-%d", i+3)
		binding.NativeSessionID = fmt.Sprintf("native-%d", i+3)
		_, err := s.BindAgent(ctx, item.token, "attach", AgentBindingRequest{Context: f.ref(0), AgentID: item.id, ExpectedRevision: 1, Runtime: &binding, OperationID: opaqueID()})
		if err != nil {
			t.Fatal(err)
		}
		f.config.Targets = append(f.config.Targets, binding)
	}
	f.write(t)
	output := os.Getenv("ASTACK_NOTICE_MEASUREMENT_OUTPUT")
	before, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	if output != "" {
		if err = os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Backup(ctx, f.operator.Token, filepath.Join(output, "before.sqlite")); err != nil {
			t.Fatal(err)
		}
	}
	latencies := make([]int64, 0, 20)
	bodyBytes := 0
	for i := 0; i < 20; i++ {
		body := fmt.Sprintf("Single %02d @03-worker", i)
		bodyBytes += len(body)
		start := time.Now()
		f.send(t, f.a.Token, body)
		if f.drain(t, 4).Submitted != 1 {
			t.Fatal("isolated send not submitted")
		}
		latencies = append(latencies, time.Since(start).Microseconds())
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	bursts := []map[string]any{}
	for _, size := range []int{100, 1000} {
		prompts := f.prompts(t)
		start := time.Now()
		for i := 0; i < size; i++ {
			body := fmt.Sprintf("Burst %04d @03-worker @01-orchestrator @04-worker", i)
			bodyBytes += len(body)
			f.send(t, f.a.Token, body)
		}
		drainStart := time.Now()
		r := f.drain(t, 16)
		submissions := f.prompts(t) - prompts
		want := 3 * ((size + 499) / 500)
		if r.Submitted != want || submissions != want {
			t.Fatal("burst did not cover exact bounded batches", r, submissions)
		}
		bursts = append(bursts, map[string]any{"messages": size, "recipients_per_message": 3, "submissions": submissions, "drain_us": time.Since(drainStart).Microseconds(), "commit_and_drain_us": time.Since(start).Microseconds()})
	}
	messages := countTest(t, s, "SELECT count(*) FROM mail_messages")
	mentions := countTest(t, s, "SELECT count(*) FROM mail_mentions")
	batches := countTest(t, s, "SELECT count(*) FROM notice_batches")
	refs := countTest(t, s, "SELECT count(*) FROM notice_batch_refs")
	submitted := countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='submitted'")
	var bytes int
	if s.db.QueryRow("SELECT sum(length(CAST(body AS BLOB))) FROM mail_messages").Scan(&bytes) != nil {
		t.Fatal("body bytes unavailable")
	}
	if messages != 1120 || mentions != 3320 || refs != 3320 || submitted != 3320 || batches != 29 || bytes != bodyBytes || f.prompts(t) != 29 {
		t.Fatal("measurement lost/duplicated bodies or refs")
	}
	after, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	measurement := map[string]any{"messages": messages, "mentions": mentions, "batch_references": refs, "batches": batches, "fake_submissions": f.prompts(t), "stored_body_bytes": bytes, "expected_body_bytes": bodyBytes, "single_send_to_fake_submission_p50_us": latencies[len(latencies)/2], "single_send_to_fake_submission_p95_us": latencies[18], "bursts": bursts, "before": before, "after": after, "sqlite_version": s.sqliteVersion, "adapter": "fake executable; no live Herdr input"}
	if output != "" {
		if _, err = s.Backup(ctx, f.operator.Token, filepath.Join(output, "after.sqlite")); err != nil {
			t.Fatal(err)
		}
		data, _ := json.MarshalIndent(measurement, "", "  ")
		if err = os.WriteFile(filepath.Join(output, "measurement.json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("1120 messages/%d body bytes, 3320 mention/ref rows, 29 fake submissions/batches; single p50=%dus p95=%dus; DB %d->%d WAL %d->%d", bytes, latencies[10], latencies[18], before.DatabaseBytes, after.DatabaseBytes, before.WALBytes, after.WALBytes)
	for _, burst := range bursts {
		t.Logf("burst: %v", burst)
	}
}
