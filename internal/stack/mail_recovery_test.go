package stack

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type crashMailConfig struct {
	StateDir       string          `json:"state_dir"`
	CredentialFile string          `json:"credential_file"`
	Request        MailSendRequest `json:"request"`
	Phase          string          `json:"phase"`
	Kill           bool            `json:"kill"`
}

func TestMailCrashHelper(t *testing.T) {
	path := os.Getenv("ASTACK_MAIL_CRASH_CONFIG")
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config crashMailConfig
	if json.Unmarshal(data, &config) != nil {
		t.Fatal("crash fixture")
	}
	service, err := Start(context.Background(), config.StateDir, "")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := ReadCredential(config.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := service.Store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	crash := func() int {
		if config.Kill {
			process, err := os.FindProcess(os.Getpid())
			if err != nil || process.Kill() != nil {
				panic("could not kill crash helper")
			}
			select {} // SIGKILL must terminate us, not a graceful SQLite close.
		}
		os.Exit(29)
		return 0
	}
	err = conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc("mail_test_crash", crash, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	table := "mail_mentions"
	switch config.Phase {
	case "body":
		table = "mail_messages"
	case "receipt":
		table = "operation_receipts"
	}
	if config.Phase != "committed" {
		_, err = conn.ExecContext(context.Background(), "CREATE TEMP TRIGGER crash_mid_mail AFTER INSERT ON main."+table+" BEGIN SELECT mail_test_crash(); END")
	}
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	_, err = service.Store.SendMail(context.Background(), credential.Token, config.Request)
	if err == nil && config.Phase == "committed" {
		crash()
	}
	t.Fatal("crash trigger was not reached", err)
}
func TestMailProcessCrashRollbackAndOriginalReceiptRecovery(t *testing.T) {
	mailCrashRecovery(t, "mention", false)
}

// Opt-in nightly workload; every seed selects a named transaction milestone.
func TestMailSeededSIGKILLRecovery(t *testing.T) {
	value := os.Getenv("A_STACK_CRASH_ITERATIONS")
	if value == "" {
		t.Skip("nightly: set A_STACK_CRASH_ITERATIONS=100")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 1000 {
		t.Fatal("A_STACK_CRASH_ITERATIONS must be 1..1000")
	}
	phases := []string{"body", "mention", "receipt", "committed"}
	for seed := range n {
		phase := phases[rand.New(rand.NewPCG(uint64(seed), 19)).IntN(len(phases))]
		t.Run(fmt.Sprintf("seed-%d-%s", seed, phase), func(t *testing.T) {
			mailCrashRecovery(t, phase, true)
		})
	}
}

func mailCrashRecovery(t *testing.T, phase string, kill bool) {
	t.Helper()
	f := mailFixture(t)
	dir := f.dir
	credentialFile := filepath.Join(privateTestDir(t), "actor.json")
	if err := writePrivate(credentialFile, f.a, false); err != nil {
		t.Fatal(err)
	}
	request := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "process-crash-mail", Body: "Atomic message @03-worker"}
	configFile := filepath.Join(privateTestDir(t), "crash.json")
	if err := writePrivate(configFile, crashMailConfig{StateDir: dir, CredentialFile: credentialFile, Request: request, Phase: phase, Kill: kill}, false); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Close(ctx); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(exe, "-test.run=^TestMailCrashHelper$")
	command.Env = append(os.Environ(), "ASTACK_MAIL_CRASH_CONFIG="+configFile)
	err = command.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || (!kill && exit.ExitCode() != 29) || (kill && exit.ExitCode() != -1) {
		t.Fatal("child did not crash between intended writes")
	}
	restarted, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	want := 0
	if phase == "committed" {
		want = 1
	}
	if countTest(t, restarted.Store, "SELECT count(*) FROM mail_messages") != want || countTest(t, restarted.Store, "SELECT count(*) FROM mail_mentions") != want || countTest(t, restarted.Store, "SELECT count(*) FROM operation_receipts WHERE operation_id='process-crash-mail'") != want {
		t.Fatal("process crash left partial body/notice/receipt")
	}
	first, err := restarted.Store.SendMail(ctx, f.a.Token, request)
	if err != nil || first.Revision != 1 {
		t.Fatal("crash recovery lost sequence", err)
	}
	// Simulate loss of a committed response followed by another service restart.
	if err = restarted.Close(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close(ctx)
	retry, err := recovered.Store.SendMail(ctx, f.a.Token, request)
	if err != nil || retry != first {
		t.Fatal("restart duplicated send or changed receipt")
	}
	page, err := recovered.Store.ReadMail(ctx, f.b.Token, MailReadRequest{Context: f.ref(0), TaskID: f.taskID})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != first.ResourceID || page.Messages[0].Body != request.Body {
		t.Fatal("restart lost shared body")
	}
	var integrity string
	if recovered.Store.db.QueryRow("PRAGMA integrity_check").Scan(&integrity) != nil || integrity != "ok" {
		t.Fatal("mail recovery integrity")
	}
}
