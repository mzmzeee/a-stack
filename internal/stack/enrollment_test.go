package stack

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareAgentCredentialRejectsCheckoutDestinations(t *testing.T) {
	checkout := privateTestDir(t)
	if err := os.Mkdir(filepath.Join(checkout, ".git"), 0700); err != nil {
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
			if err := os.Mkdir(parent, 0700); err != nil {
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
