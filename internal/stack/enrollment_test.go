package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareAgentCredentialRejectsCheckoutDestinations(t *testing.T) {
	checkout := privateTestDir(t)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}

	outside := privateTestDir(t)
	alias := filepath.Join(outside, "checkout-alias")
	if err := os.Symlink(checkout, alias); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []string{"direct", "symlink-ancestor"} {
		t.Run(kind, func(t *testing.T) {
			parent := filepath.Join(checkout, kind)
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}

			destination := filepath.Join(parent, "worker.json")
			if kind == "symlink-ancestor" {
				destination = filepath.Join(alias, kind, "worker.json")
			}

			request := AgentRegisterRequest{OperationID: "checkout-enrollment", Name: "worker"}
			_, err := PrepareAgentCredential(destination, opaqueID(), "agent register", request.OperationID, request)
			requireCode(t, err, "invalid_input")

			if _, err = os.Lstat(filepath.Join(parent, "worker.json")); !os.IsNotExist(err) {
				t.Fatal("rejected destination contains a credential")
			}

			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("rejected destination created temporary credential artifacts")
			}
		})
	}
}

// A new credential file names its Workspace, Session and Agent. A bad ID or
// name leaves the file without context, and a file with a bad one is refused.
func TestCredentialFileContext(t *testing.T) {
	dir := privateTestDir(t)
	ref := Context{WorkspaceID: opaqueID(), SessionID: opaqueID()}

	good := filepath.Join(dir, "good.json")
	request := AgentRegisterRequest{Context: ref, OperationID: "context-register", Name: "02-worker"}
	if _, err := PrepareAgentCredential(good, opaqueID(), "agent register", request.OperationID, request); err != nil {
		t.Fatal(err)
	}

	c, err := ReadCredential(good)
	if err != nil || c.WorkspaceID != ref.WorkspaceID || c.SessionID != ref.SessionID || c.AgentName != "02-worker" {
		t.Fatal("register context")
	}

	rebound := filepath.Join(dir, "rebound.json")
	rebind := AgentBindingRequest{Context: ref, OperationID: "context-rebind", AgentID: opaqueID()}
	if _, err = PrepareRebindCredential(rebound, opaqueID(), rebind, "02-worker"); err != nil {
		t.Fatal(err)
	}

	if c, err = ReadCredential(rebound); err != nil || c.SessionID != ref.SessionID || c.AgentName != "02-worker" {
		t.Fatal("rebind context")
	}

	bad := filepath.Join(dir, "bad.json")
	request = AgentRegisterRequest{Context: ref, OperationID: "context-bad-name", Name: "-bad"}
	if _, err = PrepareAgentCredential(bad, opaqueID(), "agent register", request.OperationID, request); err != nil {
		t.Fatal(err)
	}

	if c, err = ReadCredential(bad); err != nil || c.WorkspaceID != "" || c.AgentName != "" {
		t.Fatal("a bad name must leave no context")
	}

	c.WorkspaceID = "not-an-id"
	if err = writePrivate(bad, c, true); err != nil {
		t.Fatal(err)
	}

	_, err = ReadCredential(bad)
	requireCode(t, err, "invalid_input")
}
