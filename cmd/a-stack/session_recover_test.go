package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/mzmzeee/a-stack/internal/stack"
)

func TestCLIRecoverTransfer(t *testing.T) {
	f := newTeam(t)
	client, err := stack.NewClient(f.state, "", stack.OperatorCapability)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	ref := stack.Context{WorkspaceID: f.workspace, SessionID: f.session}
	id := shareToken()
	if _, err = client.Do(ctx, "POST", "/v1/session/transfer/export", stack.TransferExportRequest{Context: ref, TransferID: id, Repository: "example.com/repo"}); err != nil {
		t.Fatal(err)
	}

	if _, err = client.Do(ctx, "POST", "/v1/session/transfer/seal", stack.TransferChangeRequest{Context: ref, TransferID: id, DestinationDatabase: shareToken()}); err != nil {
		t.Fatal(err)
	}

	args := []string{"session", "recover-transfer", "--state-dir", f.state, "--workspace", f.workspace, "--session", f.session, "--transfer-id", id}
	var out, stderr bytes.Buffer
	if run(args, &out, &stderr) == 0 {
		t.Fatal("recovery without confirmation")
	}

	for range 2 {
		out.Reset()
		stderr.Reset()

		if run(append(args, "--destination-stopped"), &out, &stderr) != 0 {
			t.Fatal(stderr.String())
		}
	}

	if _, err = inspectSession(ctx, client, ref); err != nil {
		t.Fatal(err)
	}
}
