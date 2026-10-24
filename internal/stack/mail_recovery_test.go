package stack

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

type crashMailConfig struct {
	StateDir       string          `json:"state_dir"`
	CredentialFile string          `json:"credential_file"`
	Request        MailSendRequest `json:"request"`
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
	err = conn.Raw(func(raw any) error {
		return raw.(*sqlite3.SQLiteConn).RegisterFunc("mail_test_crash", func() int { os.Exit(29); return 0 }, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(context.Background(), "CREATE TEMP TRIGGER crash_mid_mail AFTER INSERT ON main.mail_mentions BEGIN SELECT mail_test_crash(); END")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	_, err = service.Store.SendMail(context.Background(), credential.Token, config.Request)
	t.Fatal("crash trigger was not reached", err)
}
func TestMailProcessCrashRollbackAndOriginalReceiptRecovery(t *testing.T) {
	f := mailFixture(t)
	dir := f.dir
	credentialFile := filepath.Join(privateTestDir(t), "actor.json")
	if err := writePrivate(credentialFile, f.a, false); err != nil {
		t.Fatal(err)
	}
	request := MailSendRequest{Context: f.ref(0), TaskID: f.taskID, OperationID: "process-crash-mail", Body: "Atomic message @03-worker"}
	configFile := filepath.Join(privateTestDir(t), "crash.json")
	if err := writePrivate(configFile, crashMailConfig{dir, credentialFile, request}, false); err != nil {
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
	if !ok || exit.ExitCode() != 29 {
		t.Fatal("child did not crash between intended writes")
	}
	restarted, err := Start(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close(ctx)
	if countTest(t, restarted.Store, "SELECT count(*) FROM mail_messages") != 0 || countTest(t, restarted.Store, "SELECT count(*) FROM mail_mentions") != 0 || countTest(t, restarted.Store, "SELECT count(*) FROM operation_receipts WHERE operation_id='process-crash-mail'") != 0 {
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
