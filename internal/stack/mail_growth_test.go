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

func TestMailGrowth10000SingleCopyBodies(t *testing.T) {
	f := mailFixture(t)
	s := f.service.Store
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 3, AgentID: f.orchID})
	_, third := f.identityFixture.register(t, 0, "04-worker")
	f.change(t, f.orch.Token, "assign", TaskChangeRequest{TaskID: f.taskID, ExpectedRevision: 4, AgentID: third.ResourceID})
	before, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("ASTACK_MAIL_GROWTH_OUTPUT")
	if output != "" {
		if err = os.MkdirAll(output, 0700); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Backup(ctx, f.operator.Token, filepath.Join(output, "before.sqlite")); err != nil {
			t.Fatal(err)
		}
	}
	latencies := make([]int64, 0, 10000)
	bodyBytes := 0
	start := time.Now()
	for i := 0; i < 10000; i++ {
		body := fmt.Sprintf("Measurement %05d @03-worker @01-orchestrator @04-worker", i)
		bodyBytes += len(body)
		begin := time.Now()
		receipt, err := s.SendMail(ctx, f.a.Token, MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: fmt.Sprintf("growth-mail-%05d", i), Body: body})
		latencies = append(latencies, time.Since(begin).Microseconds())
		if err != nil || receipt.Revision != int64(i+1) {
			t.Fatal("growth send/sequence", err)
		}
	}
	elapsed := time.Since(start)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	messages := countTest(t, s, "SELECT count(*) FROM mail_messages")
	mentions := countTest(t, s, "SELECT count(*) FROM mail_mentions")
	pending := countTest(t, s, "SELECT count(*) FROM mail_mentions WHERE notice_state='pending'")
	receipts := countTest(t, s, "SELECT count(*) FROM operation_receipts WHERE name='mail.send'")
	var storedBytes int
	if s.db.QueryRow("SELECT sum(length(CAST(body AS BLOB))) FROM mail_messages").Scan(&storedBytes) != nil {
		t.Fatal("body byte count")
	}
	if messages != 10000 || mentions != 30000 || pending != 30000 || receipts != 10000 || storedBytes != bodyBytes {
		t.Fatal("growth duplicated/lost body, mention, pending state or receipt")
	}
	if countTest(t, s, "SELECT count(*) FROM pragma_table_info('mail_mentions') WHERE name='body'") != 0 {
		t.Fatal("notice table copied message bodies")
	}
	after, err := s.Status()
	if err != nil {
		t.Fatal(err)
	}
	measurement := map[string]any{"messages": messages, "stored_body_bytes": storedBytes, "expected_body_bytes": bodyBytes, "mentions": mentions, "pending_notices": pending, "receipts": receipts, "submissions": 0, "sqlite_version": s.sqliteVersion, "before": before, "after": after, "send_p50_us": latencies[len(latencies)/2], "send_p95_us": latencies[9499], "elapsed_ms": float64(elapsed.Microseconds()) / 1000}
	if output != "" {
		if _, err = s.Backup(ctx, f.operator.Token, filepath.Join(output, "after.sqlite")); err != nil {
			t.Fatal(err)
		}
		data, _ := json.MarshalIndent(measurement, "", "  ")
		if err = os.WriteFile(filepath.Join(output, "measurement.json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Log only nonsecret fixture counts/timing/sizes. Analysis exports, when asked,
	// use completed SQLite backups, never a live main-database-only copy.
	t.Logf("10000 messages, %d body bytes, %d mentions/pending, %d receipts; %.3f ms, p50=%d us p95=%d us; db %d->%d WAL %d->%d", storedBytes, mentions, receipts, float64(elapsed.Microseconds())/1000, latencies[len(latencies)/2], latencies[9499], before.DatabaseBytes, after.DatabaseBytes, before.WALBytes, after.WALBytes)
}
