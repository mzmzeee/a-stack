//go:build bench

// Performance harness. It is excluded from `go test ./...` by the bench build
// tag and is driven by scripts/bench.py. It never touches default state: every
// entry point requires an explicit disposable directory or fixture file.
//
//	go test -c -tags bench -o /tmp/svc.test ./internal/stack
//	ASTACK_BENCH_STATE=/private/dir ASTACK_BENCH_OUT=/out /tmp/svc.test -test.run '^TestBenchServe$' -test.timeout 0
//	ASTACK_BENCH_FIXTURE=/out/fixture.json go test -tags bench -run '^$' -bench Fixture -benchmem ./internal/stack
package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestBenchServe runs the real Service in-process, like `a-stack serve`, plus a
// separate loopback pprof listener and a once-per-second stats log. The data
// listener and its rules are unchanged; pprof never shares its port.
func TestBenchServe(t *testing.T) {
	dir := os.Getenv("ASTACK_BENCH_STATE")
	out := os.Getenv("ASTACK_BENCH_OUT")
	if dir == "" || out == "" {
		t.Skip("set ASTACK_BENCH_STATE and ASTACK_BENCH_OUT")
	}
	maxSeconds, _ := strconv.Atoi(os.Getenv("ASTACK_BENCH_MAX_SECONDS"))
	if maxSeconds <= 0 {
		maxSeconds = 3600
	}
	if rate, _ := strconv.Atoi(os.Getenv("ASTACK_BENCH_BLOCK_RATE")); rate > 0 {
		runtime.SetBlockProfileRate(rate)
		runtime.SetMutexProfileFraction(100)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	TuneServiceGC() // as serve does
	service, err := Start(ctx, dir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- service.Serve() }()
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	profiler, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go http.Serve(profiler, mux)
	statsFile, err := os.Create(filepath.Join(out, "service-stats.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer statsFile.Close()
	go func() {
		encoder := json.NewEncoder(statsFile)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for range tick.C {
			db := service.Store.db.Stats()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			_ = encoder.Encode(map[string]any{
				"t": float64(time.Now().UnixNano()) / 1e9, "db_in_use": db.InUse, "db_wait_count": db.WaitCount,
				"db_wait_ms": float64(db.WaitDuration.Microseconds()) / 1000, "heap_alloc": m.HeapAlloc, "heap_sys": m.HeapSys,
				"heap_objects": m.HeapObjects, "total_alloc": m.TotalAlloc, "mallocs": m.Mallocs, "num_gc": m.NumGC,
				"gc_pause_total_ms": float64(m.PauseTotalNs) / 1e6, "goroutines": runtime.NumGoroutine(),
			})
		}
	}()
	ready := map[string]any{"data": map[string]any{"status": "ready", "url": service.Discovery.URL, "pprof": "http://" + profiler.Addr().String(), "pid": os.Getpid()}}
	b, _ := json.Marshal(ready)
	fmt.Fprintln(os.Stdout, string(b))
	select {
	case <-ctx.Done():
	case err = <-result:
	case <-time.After(time.Duration(maxSeconds) * time.Second):
	}
	profiler.Close()
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if closeErr := service.Close(closeCtx); closeErr != nil || err != nil {
		t.Fatal(closeErr, err)
	}
}

// TestBenchQueryPlans runs EXPLAIN QUERY PLAN and timed executions with the
// bundled SQLite engine on a disposable copy of a completed backup. Every
// execution runs in a transaction that is rolled back, so UPDATE probes leave
// the copy unchanged. With "analyze" it first runs ANALYZE on that copy.
func TestBenchQueryPlans(t *testing.T) {
	path := os.Getenv("ASTACK_BENCH_QUERIES")
	if path == "" {
		t.Skip("set ASTACK_BENCH_QUERIES")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Database string `json:"database"`
		Analyze  bool   `json:"analyze"`
		Out      string `json:"out"`
		Runs     int    `json:"runs"`
		Queries  map[string]struct {
			SQL    string `json:"sql"`
			Params []any  `json:"params"`
		} `json:"queries"`
	}
	if err = json.Unmarshal(data, &in); err != nil {
		t.Fatal(err)
	}
	if in.Runs < 6 {
		in.Runs = 25
	}
	ctx := context.Background()
	s, err := openStore(ctx, in.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if in.Analyze {
		if _, err = s.db.ExecContext(ctx, "ANALYZE"); err != nil {
			t.Fatal(err)
		}
	}
	out := map[string]any{"sqlite": s.sqliteVersion, "analyze": in.Analyze}
	plans := map[string]any{}
	for name, q := range in.Queries {
		rows, err := s.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q.SQL, q.Params...)
		if err != nil {
			plans[name] = map[string]any{"error": err.Error()}
			continue
		}
		plan := []string{}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		samples := []float64{}
		for i := 0; i < in.Runs; i++ {
			tx, err := s.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			r, err := tx.QueryContext(ctx, q.SQL, q.Params...)
			if err != nil {
				tx.Rollback()
				t.Fatal(name, err)
			}
			for r.Next() {
			}
			r.Close()
			samples = append(samples, float64(time.Since(start).Microseconds())/1000)
			tx.Rollback()
		}
		samples = samples[5:]
		sort.Float64s(samples)
		plans[name] = map[string]any{"plan": plan, "n": len(samples), "p50_ms": samples[len(samples)/2], "p95_ms": samples[len(samples)*95/100]}
	}
	out["queries"] = plans
	b, _ := json.MarshalIndent(out, "", " ")
	if err = os.WriteFile(in.Out, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// benchFixture is written by scripts/bench.py next to a restored copy of a
// completed backup. The tokens are synthetic and the files are disposable.
type benchFixture struct {
	Database    string `json:"database"`
	WorkspaceID string `json:"workspace_id"`
	SessionID   string `json:"session_id"`
	Operator    string `json:"operator_token"`
	Agent       string `json:"agent_token"`
	AgentName   string `json:"agent_name"`
	TaskID      string `json:"task_id"`
	PeerName    string `json:"peer_name"`
}

func loadBenchFixture(b *testing.B) (benchFixture, *Store) {
	path := os.Getenv("ASTACK_BENCH_FIXTURE")
	if path == "" {
		b.Skip("set ASTACK_BENCH_FIXTURE")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	var f benchFixture
	if err = json.Unmarshal(data, &f); err != nil {
		b.Fatal(err)
	}
	s, err := openStore(context.Background(), f.Database)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.db.Close() })
	return f, s
}
func (f benchFixture) ref() Context {
	return Context{WorkspaceID: f.WorkspaceID, SessionID: f.SessionID}
}

func BenchmarkFixtureAuthenticate(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := authenticate(ctx, s.db, f.Agent); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkFixtureSnapshotUnchanged(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	first, err := s.Snapshot(ctx, f.Operator, SnapshotRequest{Context: f.ref()})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Snapshot(ctx, f.Operator, SnapshotRequest{Context: f.ref(), KnownRevision: first.Session.ReadRevision}); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkFixtureSnapshotCounts(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := s.Snapshot(ctx, f.Operator, SnapshotRequest{Context: f.ref()}); err != nil {
			b.Fatal(err)
		}
	}
}
func benchPage(b *testing.B, s *Store, f benchFixture, kind string, limit int) {
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		page, err := s.SnapshotPage(ctx, f.Operator, SnapshotPageRequest{Context: f.ref(), Kind: kind, Limit: limit})
		if err != nil {
			b.Fatal(err)
		}
		if _, err = json.Marshal(map[string]any{"data": page}); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkFixtureTaskPage100(b *testing.B) {
	f, s := loadBenchFixture(b)
	benchPage(b, s, f, "tasks", 100)
}
func BenchmarkFixtureAssignmentPage100(b *testing.B) {
	f, s := loadBenchFixture(b)
	benchPage(b, s, f, "assignments", 100)
}
func BenchmarkFixtureDependencyPage100(b *testing.B) {
	f, s := loadBenchFixture(b)
	benchPage(b, s, f, "dependencies", 100)
}

// One browser "changed poll": snapshot plus every page of the four collections.
func BenchmarkFixtureFullCollection(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	b.ReportAllocs()
	pages := 0
	for i := 0; i < b.N; i++ {
		snap, err := s.Snapshot(ctx, f.Operator, SnapshotRequest{Context: f.ref()})
		if err != nil {
			b.Fatal(err)
		}
		pages = 1
		for _, kind := range []string{"tasks", "agents", "assignments", "dependencies"} {
			after := ""
			for {
				page, err := s.SnapshotPage(ctx, f.Operator, SnapshotPageRequest{Context: f.ref(), Kind: kind, Limit: 100, After: after, ReadRevision: snap.Session.ReadRevision})
				if err != nil {
					b.Fatal(err)
				}
				if _, err = json.Marshal(map[string]any{"data": page}); err != nil {
					b.Fatal(err)
				}
				pages++
				if page.Next == "" {
					break
				}
				after = page.Next
			}
		}
	}
	b.ReportMetric(float64(pages), "requests/op")
}
func BenchmarkFixtureShowTask(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := s.ShowTask(ctx, f.Agent, TaskReadRequest{Context: f.ref(), TaskID: f.TaskID}); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkFixtureMailReadFirst100(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		page, err := s.ReadMail(ctx, f.Agent, MailReadRequest{Context: f.ref(), TaskID: f.TaskID, Limit: 100})
		if err != nil {
			b.Fatal(err)
		}
		if _, err = json.Marshal(map[string]any{"data": page}); err != nil {
			b.Fatal(err)
		}
	}
}

// mailCheckRound follows next_task until the scan ends and merges cursors, as
// docs/mail.md tells an Agent to do. It returns the number of requests.
func mailCheckRound(b *testing.B, s *Store, f benchFixture, cursors map[string]string) (int, int) {
	ctx := context.Background()
	after, calls, messages := "", 0, 0
	for {
		page, err := s.CheckMail(ctx, f.Agent, MailCheckRequest{Context: f.ref(), Cursors: cursors, AfterTask: after, TaskLimit: 100, Limit: 500})
		if err != nil {
			b.Fatal(err)
		}
		if _, err = json.Marshal(map[string]any{"data": page}); err != nil {
			b.Fatal(err)
		}
		calls++
		for _, task := range page.Tasks {
			cursors[task.TaskID] = task.Cursor
			messages += len(task.Messages)
		}
		if page.NextTask == "" {
			return calls, messages
		}
		after = page.NextTask
	}
}

// Steady state: every cursor is already at the end, so nothing new is returned.
func BenchmarkFixtureMailCheckIdleRound(b *testing.B) {
	f, s := loadBenchFixture(b)
	cursors := map[string]string{}
	for {
		if _, messages := mailCheckRound(b, s, f, cursors); messages == 0 {
			break
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	calls := 0
	for i := 0; i < b.N; i++ {
		calls, _ = mailCheckRound(b, s, f, cursors)
	}
	b.ReportMetric(float64(calls), "requests/op")
	b.ReportMetric(float64(len(cursors)), "tasks")
}

// Cold catch-up for an Agent with no cursor file: rounds until no messages.
func BenchmarkFixtureMailCheckColdCatchUp(b *testing.B) {
	f, s := loadBenchFixture(b)
	b.ReportAllocs()
	calls, messages := 0, 0
	for i := 0; i < b.N; i++ {
		cursors := map[string]string{}
		calls, messages = 0, 0
		for {
			c, m := mailCheckRound(b, s, f, cursors)
			calls += c
			messages += m
			if m == 0 {
				break
			}
		}
	}
	b.ReportMetric(float64(calls), "requests/op")
	b.ReportMetric(float64(messages), "messages/op")
}
func BenchmarkFixtureMailSend(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	prefix := opaqueID()[:12]
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := s.SendMail(ctx, f.Agent, MailSendRequest{Context: f.ref(), OperationID: fmt.Sprintf("bench-%s-%d", prefix, i), TaskID: f.TaskID, Body: "Benchmark progress note for @" + f.PeerName + " with a short realistic body."})
		if err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkFixtureTaskStatus(b *testing.B) {
	f, s := loadBenchFixture(b)
	ctx := context.Background()
	prefix := opaqueID()[:12]
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		detail, err := s.ShowTask(ctx, f.Agent, TaskReadRequest{Context: f.ref(), TaskID: f.TaskID})
		if err != nil {
			b.Fatal(err)
		}
		if detail.Progress == "finished" || detail.Progress == "cancelled" {
			b.Fatal("fixture Task must stay open")
		}
		_, err = s.ChangeTask(ctx, f.Agent, "status", TaskChangeRequest{Context: f.ref(), OperationID: fmt.Sprintf("bench-%s-%d", prefix, i), TaskID: f.TaskID, ExpectedRevision: detail.Revision, Progress: "working", Reason: fmt.Sprintf("bench step %d", i)})
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkClientSetup is the per-process client cost of `a-stack` before any
// network I/O: reading and checking the credential and the discovery file.
func BenchmarkClientSetup(b *testing.B) {
	dir := b.TempDir()
	if err := writePrivate(filepath.Join(dir, "service.json"), Discovery{URL: "http://127.0.0.1:1", DatabaseID: opaqueID(), InstanceID: opaqueID()}, true); err != nil {
		b.Fatal(err)
	}
	credential := filepath.Join(dir, "agent.json")
	if err := writePrivate(credential, Credential{Kind: AgentCapability, Token: opaqueID()}, true); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := NewClient(dir, credential, AgentCapability); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkClientHealthRoundTrip is one CLI-shaped request without process
// start: a fresh client (no keep-alive) against an in-process service.
func BenchmarkClientHealthRoundTrip(b *testing.B) {
	dir := filepath.Join(b.TempDir(), "state")
	service, err := Start(context.Background(), dir, "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go service.Serve()
	b.Cleanup(func() { service.Close(context.Background()) })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		client, err := NewClient(dir, "", OperatorCapability)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = client.Do(context.Background(), "GET", "/v1/health", nil); err != nil {
			b.Fatal(err)
		}
	}
}
