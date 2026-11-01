package stack

import (
	"strings"
	"testing"
)

// Columns: service owner, invited Operator, Orchestrator, assigned worker,
// foreign Agent, foreign Operator, browser. Missing/bad/revoked bearer is 401.
// Named refusals describe this fixture's prerequisites, not permission grants.
var endpointMatrix = []struct{ routes, outcomes string }{
	{"credential", "allowed allowed allowed allowed allowed allowed forbidden"},
	{"/assets/app.js /assets/app.css /view/{workspace}/{session}", "allowed allowed allowed allowed allowed allowed allowed"},
	{"health", "allowed allowed allowed allowed allowed allowed forbidden"},
	{"storage storage/backup workspace/create connect/code session/link session/links", "allowed forbidden forbidden forbidden forbidden forbidden forbidden"},
	{"workspace/orders/show", "allowed allowed allowed allowed forbidden allowed forbidden"},
	{"workspace/list", "allowed allowed allowed allowed forbidden forbidden forbidden"},
	{"workspace/orders/set workspace/orders/clear", "allowed forbidden forbidden forbidden forbidden forbidden forbidden"},
	{"session/create", "allowed forbidden forbidden forbidden forbidden forbidden forbidden"},
	{"session/list", "allowed allowed allowed allowed forbidden forbidden forbidden"},
	{"session/inspect agent/list notifications/status", "allowed allowed allowed allowed forbidden forbidden forbidden"},
	{"session/close session/appoint session/invite browser/open browser/revoke", "allowed allowed forbidden forbidden forbidden forbidden forbidden"},
	{"session/reopen", "allowed allowed forbidden forbidden forbidden forbidden forbidden"},
	{"agent/register", "allowed allowed allowed forbidden forbidden forbidden forbidden"},
	{"agent/attach herdr/bind", "forbidden forbidden forbidden allowed forbidden forbidden forbidden"},
	{"agent/rebind", "allowed allowed forbidden forbidden forbidden forbidden forbidden"},
	{"agent/observe", "allowed allowed forbidden allowed forbidden forbidden forbidden"},
	{"task/create", "forbidden forbidden allowed forbidden forbidden forbidden forbidden"},
	{"task/show task/claims task/list task/history task/assignees task/references task/dependencies session/snapshot session/page", "allowed allowed allowed allowed forbidden forbidden allowed"},
	{"task/edit task/assign task/unassign dependency/add", "forbidden forbidden allowed forbidden forbidden forbidden forbidden"},
	{"dependency/remove", "forbidden forbidden not_found forbidden forbidden forbidden forbidden"},
	{"task/status task/report task/attention/resolve task/reference/add task/reference/remove task/claim task/unclaim", "forbidden forbidden allowed allowed forbidden forbidden forbidden"},
	{"task/attention/set", "forbidden forbidden conflict conflict forbidden forbidden forbidden"},
	{"task/reopen", "forbidden forbidden conflict conflict forbidden forbidden forbidden"},
	{"task/archive task/unarchive", "conflict conflict conflict forbidden forbidden forbidden forbidden"},
	{"task/answer", "allowed allowed forbidden forbidden forbidden forbidden allowed"},
	{"mail/send mail/reply", "forbidden forbidden allowed allowed forbidden forbidden forbidden"},
	{"mail/ack", "forbidden forbidden forbidden allowed forbidden forbidden forbidden"},
	{"mail/read", "forbidden forbidden allowed allowed forbidden forbidden allowed"},
	{"mail/recipients", "forbidden forbidden allowed allowed forbidden forbidden forbidden"},
	{"mail/check mail/urgent", "forbidden forbidden allowed allowed forbidden forbidden forbidden"},
	{"relay/claim relay/begin relay/result", "forbidden forbidden forbidden allowed forbidden forbidden forbidden"},
	{"notifications/drain", "integration_unavailable forbidden forbidden forbidden forbidden forbidden forbidden"},
	{"herdr/focus", "integration_unavailable forbidden forbidden forbidden forbidden forbidden forbidden"},
	// These three authenticate a one-time code (and trusted Serve login for join),
	// not the bearer. The matrix supplies a valid code independently of the caller.
	{"browser/redeem connect/join session/join", "allowed allowed allowed allowed allowed allowed allowed"},
	{"session/transfer/status", "allowed allowed forbidden forbidden forbidden forbidden forbidden"},
	{"session/transfer/export session/transfer/seal", "allowed forbidden forbidden forbidden forbidden forbidden forbidden"},
	{"session/transfer/import", "allowed forbidden forbidden forbidden forbidden forbidden forbidden"},
	{"session/transfer/activate session/transfer/recover", "conflict forbidden forbidden forbidden forbidden forbidden forbidden"},
}

func matrixEndpointRows(t *testing.T) map[string][]string {
	t.Helper()

	rows := map[string][]string{}
	for _, group := range endpointMatrix {
		outcomes := strings.Fields(group.outcomes)
		if len(outcomes) != 7 {
			t.Fatal("matrix needs seven authenticated caller cells", group.routes)
		}

		for _, path := range strings.Fields(group.routes) {
			if !strings.HasPrefix(path, "/") {
				path = "/v1/" + path
			}

			if rows[path] != nil {
				t.Fatal("duplicate endpoint row", path)
			}

			rows[path] = outcomes
		}
	}

	routes := matrixRoutes(t)
	for _, path := range routes {
		if rows[path] == nil {
			t.Errorf("unlisted router endpoint %s: add caller matrix row", path)
		}
	}

	for path := range rows {
		found := false
		for _, actual := range routes {
			if path == actual {
				found = true
			}
		}

		if !found {
			t.Errorf("matrix endpoint no longer in router: %s", path)
		}
	}

	if t.Failed() {
		t.FailNow()
	}

	return rows
}

func TestEndpointCallerMatrix(t *testing.T) {
	rows := matrixEndpointRows(t)
	f := newMatrixFixture(t)
	for _, route := range matrixRoutes(t) {
		for i, caller := range matrixCallers {
			t.Run(strings.TrimPrefix(route, "/v1/")+"/"+caller, func(t *testing.T) {
				f.reset(t)

				want := "unauthorized"
				if i >= 2 && i <= 8 {
					want = rows[route][i-2]
				}

				public := route == "/v1/browser/redeem" || route == "/v1/session/join" || route == "/v1/connect/join"
				if public || !strings.HasPrefix(route, "/v1/") {
					want = "allowed"
				}

				got, reads := f.call(t, route, caller)
				if got != want {
					t.Errorf("want %s, got %s", want, got)
				}

				if (route == "/v1/session/transfer/import" || route == "/v1/session/transfer/export") && caller != "owner" && reads != 0 {
					t.Errorf("refused transfer read body %d times", reads)
				}

				if strings.HasPrefix(route, "/v1/relay/") && (caller == "owner" || caller == "friend" || caller == "foreign-operator" || caller == "browser" || caller == "none" || caller == "bad" || caller == "revoked") && reads != 0 {
					t.Errorf("refused relay read body %d times", reads)
				}

				if public {
					t.Run("invalid-code", func(t *testing.T) {
						f.reset(t)
						invite, owner, view := f.inviteCode, f.ownerCode, f.viewCode
						defer func() { f.inviteCode, f.ownerCode, f.viewCode = invite, owner, view }()
						f.inviteCode, f.ownerCode, f.viewCode = opaqueID(), opaqueID(), opaqueID()

						want := "forbidden"
						if route == "/v1/browser/redeem" {
							want = "unauthorized"
						}

						if got, _ := f.call(t, route, caller); got != want {
							t.Errorf("invalid code: want %s, got %s", want, got)
						}
					})
				}
			})
		}
	}
}
