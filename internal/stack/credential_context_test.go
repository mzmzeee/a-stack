package stack

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// GET /v1/credential tells an Agent token its own Workspace, Session and
// Agent, which a credential file from before v0.1.5 does not name.
func TestCredentialContextNamesTheTokensOwnSession(t *testing.T) {
	f := identities(t)
	worker, receipt := f.register(t, 1, "02-worker")
	read := func(c Credential, method string) (int, CredentialContext, string) {
		t.Helper()
		status, data := rawRequest(t, f.service, c, method, "/v1/credential", nil, nil)
		var envelope struct {
			Data CredentialContext `json:"data"`
		}
		_ = json.Unmarshal(data, &envelope)
		return status, envelope.Data, string(data)
	}

	status, got, raw := read(worker, http.MethodGet)
	want := CredentialContext{Kind: AgentCapability, WorkspaceID: f.workspace, SessionID: f.sessions[1], AgentID: receipt.ResourceID, AgentName: "02-worker"}
	if status != http.StatusOK || got != want {
		t.Fatalf("agent context %d %s", status, raw)
	}

	if status, got, raw = read(f.operator, http.MethodGet); status != http.StatusOK || got != (CredentialContext{Kind: OperatorCapability}) {
		t.Fatalf("operator context %d %s", status, raw)
	}

	if status, _, raw = read(worker, http.MethodPost); status == http.StatusOK || !strings.Contains(raw, "GET required") {
		t.Fatalf("POST %d %s", status, raw)
	}

	browser := issueTest(t, f.service.Store, BrowserCapability, f.workspace, f.sessions[1])
	if status, _, raw = read(browser, http.MethodGet); !strings.Contains(raw, `"forbidden"`) {
		t.Fatalf("browser %d %s", status, raw)
	}

	if status, _, raw = read(Credential{Token: opaqueID()}, http.MethodGet); !strings.Contains(raw, `"unauthorized"`) {
		t.Fatalf("unknown token %d %s", status, raw)
	}
}
