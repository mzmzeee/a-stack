package stack

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

func TestCrossCommandOperationConflictsDoNotReplayOtherResourceTypes(t *testing.T) {
	f := identities(t)

	_, agent := f.register(t, 0, "cross-type-agent")
	cases := []struct {
		name, path, resourceID string
		payload                any
	}{
		{"workspace-create-to-session-create", "/v1/session/create", f.workspace, SessionCreateRequest{OperationID: "workspace-create", WorkspaceID: f.workspace, Name: "Collision"}},
		{"agent-register-to-session-appoint", "/v1/session/appoint", agent.ResourceID, SessionChangeRequest{Context: f.ref(0), OperationID: "register-cross-type-agent", AgentID: agent.ResourceID, ExpectedRevision: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}

			status, response := rawRequest(t, f.service, f.operator, "POST", tc.path, bytes.NewReader(body), map[string]string{"Content-Type": "application/json"})
			var envelope map[string]json.RawMessage
			if json.Unmarshal(response, &envelope) != nil {
				t.Fatal("invalid error JSON")
			}

			var result Error
			if json.Unmarshal(envelope["error"], &result) != nil {
				t.Fatal("invalid structured error")
			}

			if status != 409 || result.Code != "operation_conflict" {
				t.Fatalf("cross-command collision: HTTP %d, code %s", status, result.Code)
			}

			if len(envelope) != 1 || envelope["data"] != nil || bytes.Contains(response, []byte(tc.resourceID)) {
				t.Fatal("changed request disclosed cached resource data")
			}
		})
	}

	session, err := f.service.Store.InspectSession(ctx, f.operator.Token, f.ref(0))
	if err != nil || session.OrchestratorID != nil || session.Revision != 1 {
		t.Fatal("colliding appointment changed Session")
	}

	if countTest(t, f.service.Store, "SELECT count(*) FROM sessions") != 2 {
		t.Fatal("colliding creation wrote a Session")
	}
}

func TestDigestConflictPrecedesTypedReplayButNotAuthentication(t *testing.T) {
	s, c := transactionFixture(t)
	op := Operation{ID: "explicit-retry-order", Name: "test.create", Scope: "session", Payload: json.RawMessage(`{"value":"original"}`)}

	original, err := s.Mutate(ctx, c.Token, op, replayAccess, testApply("stored-once", false))
	if err != nil {
		t.Fatal(err)
	}

	execTest(t, s, "UPDATE test_access SET allowed=0")
	replayCalls := 0
	currentAccess := func(ctx context.Context, tx *sql.Tx, p Principal, r Receipt) error {
		replayCalls++
		return replayAccess(ctx, tx, p, r)
	}
	neverApply := func(context.Context, *sql.Tx, Principal) (Receipt, error) {
		t.Error("cached or conflicting request invoked apply")
		return Receipt{}, storageError()
	}
	// A matching request must still authorize current resource read access.
	receipt, err := s.Mutate(ctx, c.Token, op, currentAccess, neverApply)
	requireCode(t, err, "forbidden")

	if receipt != (Receipt{}) || replayCalls != 1 {
		t.Fatal("revoked read returned a receipt or skipped read authorization")
	}

	// A mismatch has no typed replay or resource data, even after read revocation.
	changed := op
	changed.Payload = json.RawMessage(`{"value":"changed"}`)
	replayCalls = 0
	receipt, err = s.Mutate(ctx, c.Token, changed, currentAccess, neverApply)
	requireCode(t, err, "operation_conflict")

	if receipt != (Receipt{}) || replayCalls != 0 {
		t.Fatal("mismatch invoked typed replay or returned cached data")
	}

	execTest(t, s, "DELETE FROM capabilities WHERE token_hash=?", tokenHash(c.Token))
	// Invalidated credentials fail authentication for both matching and changed
	// requests before receipt lookup, so no conflict or cached outcome is exposed.
	for _, request := range []Operation{op, changed} {
		receipt, err = s.Mutate(ctx, c.Token, request, currentAccess, neverApply)
		requireCode(t, err, "unauthorized")

		if receipt != (Receipt{}) || replayCalls != 0 {
			t.Fatal("revoked credential reached replay or returned data")
		}
	}

	if original.ResourceID == "" || countTest(t, s, "SELECT count(*) FROM test_objects") != 1 || countTest(t, s, "SELECT count(*) FROM operation_receipts") != 1 {
		t.Fatal("conflicts/revocation changed committed data")
	}
}
