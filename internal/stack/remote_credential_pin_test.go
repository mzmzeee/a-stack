package stack

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestRemoteClientCredentialPin(t *testing.T) {
	root := privateTestDir(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("AppData", root)
	database := opaqueID()
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		if r.Header.Get("Authorization") != "" {
			t.Error("unexpected bearer during discovery")
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"data": RemoteHealth{DatabaseID: database, InstanceID: opaqueID(), Version: "test"}})
	}))
	defer server.Close()

	if _, err := connectRemote(ctx, "team", server.URL, server.Client()); err != nil {
		t.Fatal(err)
	}

	for _, kind := range []CapabilityKind{OperatorCapability, AgentCapability} {
		for _, mode := range []string{"different-database", "missing-enrollment", "different-alias", "matching", "empty-alias"} {
			t.Run(string(kind)+"/"+mode, func(t *testing.T) {
				c := Credential{Kind: kind, Token: opaqueID(), Remote: "team", Enrollment: &Enrollment{DatabaseID: database}}
				switch mode {
				case "different-database":
					c.Enrollment.DatabaseID = opaqueID()
				case "missing-enrollment":
					c.Enrollment = nil
				case "different-alias":
					c.Remote = "old"
				case "empty-alias":
					c.Remote = ""
				}

				file := filepath.Join(root, "credential.json")
				if err := writePrivate(file, c, true); err != nil {
					t.Fatal(err)
				}

				requests = 0

				_, err := newRemoteClient(ctx, "team", file, kind, server.Client())
				valid := mode == "matching" || mode == "empty-alias"
				if valid {
					if err != nil || requests != 1 {
						t.Fatal("matching credential failed", err, requests)
					}
				} else {
					if err == nil || requests != 0 {
						t.Fatal("credential mismatch reached remote", mode, requests, err)
					}

					requireCode(t, err, "conflict")
				}
			})
		}
	}
}
