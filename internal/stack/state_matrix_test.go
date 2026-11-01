package stack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	matrixSessionStates = []string{"open", "closed", "sealed", "staged", "linked"}
	matrixTaskStates    = []string{"waiting", "working", "finished", "cancelled", "archived"}
)

type stateMatrixRow struct {
	commands string
	route    string
	caller   string
	// Five outcomes in the order above. Task states run in an open Session;
	// Session states run with a waiting Task. This keeps the two axes independent.
	sessions, tasks string
}

// Policy question: terminal/archived Tasks still accept definition, report,
// reference and mail writes. Record that behavior, rather than silently imposing
// the stricter status/claim rule on those commands.
var stateMatrix = []stateMatrixRow{
	{"session inspect", "session/inspect", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"session close", "session/close", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"session reopen", "session/reopen", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"session appoint", "session/appoint", "owner", "allowed session_closed conflict conflict allowed", "allowed"},
	{"session invite", "session/invite", "friend", "allowed allowed conflict conflict allowed", "allowed"},
	{"session share", "session/transfer/export", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"session unshare", "session/transfer/export", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"session recover-transfer", "session/transfer/recover", "owner", "conflict conflict allowed conflict conflict", "conflict"},
	{"session link", "session/link", "owner", "allowed", "allowed"},
	{"session create|new", "session/create", "owner", "allowed", "allowed"},
	{"session list", "session/list", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"workspace create", "workspace/create", "owner", "allowed", "allowed"},
	{"workspace list", "workspace/list", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"workspace orders show", "workspace/orders/show", "owner", "allowed", "allowed"},
	{"workspace orders set", "workspace/orders/set", "owner", "allowed", "allowed"},
	{"workspace orders clear", "workspace/orders/clear", "owner", "allowed", "allowed"},
	{"hook enroll|hook status", "session/inspect", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"agent register", "agent/register", "orchestrator", "allowed session_closed conflict conflict allowed", "allowed"},
	{"agent list", "agent/list", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"agent attach", "agent/attach", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"agent rebind", "agent/rebind", "owner", "allowed session_closed conflict conflict allowed", "allowed"},
	{"agent observe", "agent/observe", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task create", "task/create", "orchestrator", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task list", "task/list", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task show|task handoff", "task/show", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task claims", "task/claims", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task history", "task/history", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task assignees", "task/assignees", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task references", "task/references", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task dependencies", "task/dependencies", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"task edit", "task/edit", "orchestrator", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task assign", "task/assign", "orchestrator", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task unassign", "task/unassign", "orchestrator", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task status", "task/status", "worker", "allowed session_closed conflict conflict allowed", "allowed allowed conflict conflict conflict"},
	{"task reopen", "task/reopen", "worker", "conflict session_closed conflict conflict conflict", "conflict conflict allowed allowed allowed"},
	{"task archive", "task/archive", "owner", "conflict session_closed conflict conflict conflict", "conflict conflict allowed allowed conflict"},
	{"task unarchive", "task/unarchive", "owner", "conflict session_closed conflict conflict conflict", "conflict conflict conflict conflict allowed"},
	{"task report", "task/report", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task attention set", "task/attention/set", "worker", "conflict session_closed conflict conflict conflict", "conflict"},
	{"task attention resolve", "task/attention/resolve", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task reference add", "task/reference/add", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task reference remove", "task/reference/remove", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"task claim", "task/claim", "worker", "allowed session_closed conflict conflict allowed", "allowed allowed conflict conflict conflict"},
	{"task unclaim", "task/unclaim", "worker", "allowed session_closed conflict conflict allowed", "allowed allowed conflict conflict conflict"},
	{"dependency add", "dependency/add", "orchestrator", "allowed session_closed conflict conflict allowed", "allowed"},
	{"dependency remove", "dependency/remove", "orchestrator", "not_found session_closed conflict conflict not_found", "not_found"},
	{"dependency history", "session/page", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"session snapshot|status|watch|statusline", "session/snapshot", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"session page|session export", "session/page", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"answer", "task/answer", "owner", "allowed session_closed conflict conflict allowed", "allowed"},
	{"mail send", "mail/send", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"mail reply", "mail/reply", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"mail ack", "mail/ack", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"mail read", "mail/read", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"mail recipients", "mail/recipients", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"mail check", "mail/check", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"herdr bind", "herdr/bind", "worker", "allowed session_closed conflict conflict allowed", "allowed"},
	{"herdr focus", "herdr/focus", "owner", "integration_unavailable integration_unavailable conflict conflict integration_unavailable", "integration_unavailable"},
	{"notifications status", "notifications/status", "worker", "allowed allowed conflict conflict allowed", "allowed"},
	{"notifications drain", "notifications/drain", "owner", "integration_unavailable integration_unavailable conflict conflict integration_unavailable", "integration_unavailable"},
	{"open", "browser/open", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"view revoke", "browser/revoke", "owner", "allowed allowed conflict conflict allowed", "allowed"},
	{"health", "health", "owner", "allowed", "allowed"},
	{"storage status", "storage", "owner", "allowed", "allowed"},
	{"storage backup", "storage/backup", "owner", "allowed", "allowed"},
	{"connect-code", "connect/code", "owner", "allowed", "allowed"},
}

// These commands manage the client machine or establish a long-running protocol,
// rather than accepting Session/Task state. Inventory them without installing,
// updating or starting services on the machine running go test.
var matrixLocalCommands = []string{
	"serve", "version", "connect", "update", "service install", "service uninstall", "service status",
	"skill install", "skill uninstall", "hook install", "hook uninstall", "hook", "mcp",
	"completion bash", "completion zsh", "completion fish", "completion powershell", "completion install", "completion uninstall",
}

// Commands with spaces are separated by |, unlike routes/outcome words.
func matrixCommands(t *testing.T) []string {
	t.Helper()
	found := map[string]bool{}

	files, err := filepath.Glob("../../cmd/a-stack/*.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok {
				continue
			}

			for _, spec := range gen.Specs {
				v, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}

				for i, name := range v.Names {
					if i >= len(v.Values) {
						continue
					}

					switch name.Name {
					case "lifecycleCommands", "taskCommands", "mailCommands":
						ast.Inspect(v.Values[i], func(n ast.Node) bool {
							lit, ok := n.(*ast.BasicLit)
							if ok && lit.Kind == token.STRING {
								s, _ := strconv.Unquote(lit.Value)
								found[s] = true
							}

							return true
						})
					case "helpGroups":
						ast.Inspect(v.Values[i], func(n ast.Node) bool {
							row, ok := n.(*ast.CompositeLit)
							if !ok || len(row.Elts) != 3 {
								return true
							}

							lit, ok := row.Elts[0].(*ast.BasicLit)
							if ok && lit.Kind == token.STRING {
								s, _ := strconv.Unquote(lit.Value)
								if s != "" {
									found[s] = true
								}
							}

							return true
						})
					}
				}
			}
		}
	}

	out := []string{}
	for name := range found {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

func matrixStateInventory(t *testing.T, f *matrixFixture) {
	t.Helper()
	// Inspect the live schema, not a migration constant that a later migration
	// could replace. closed and archived are flags, linked is a separate relation.
	for _, tc := range []struct{ table, column, want string }{
		{"tasks", "progress", strings.Join(matrixTaskStates[:len(matrixTaskStates)-1], " ")},
		{"sessions", "closed", "0 1"},
		{"session_locations", "state", "staged sealed active"},
	} {
		var ddl string
		if err := f.service.Store.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", tc.table).Scan(&ddl); err != nil {
			t.Fatal(err)
		}

		re := regexp.MustCompile(`(?i)` + tc.column + `\s+IN\s*\(([^)]+)\)`)
		match := re.FindStringSubmatch(ddl)
		if len(match) != 2 {
			t.Fatalf("%s.%s state constraint changed: update matrix inventory", tc.table, tc.column)
		}

		actual := strings.Fields(strings.NewReplacer("'", "", ",", " ").Replace(match[1]))
		expected := strings.Fields(tc.want)
		slices.Sort(actual)
		slices.Sort(expected)

		if !slices.Equal(actual, expected) {
			t.Fatalf("unlisted %s.%s state: got %v, matrix %v", tc.table, tc.column, actual, expected)
		}
	}
}

func (f *matrixFixture) state(t *testing.T, session, task string) {
	t.Helper()

	switch session {
	case "closed":
		execTest(t, f.service.Store, "UPDATE sessions SET closed=1 WHERE id=?", f.sessions[0])
	case "sealed", "staged":
		direction := "out"
		if session == "staged" {
			direction = "in"
		}

		execTest(t, f.service.Store, "UPDATE session_transfers SET state=?,direction=? WHERE id=?", session, direction, f.transfer)
		execTest(t, f.service.Store, "INSERT INTO session_locations(session_id,transfer_id,state,peer_database,remote) VALUES(?,?,?,?,?)", f.sessions[0], f.transfer, session, opaqueID(), "matrix")
	case "linked":
		// A link does not turn its destination into a new lifecycle state. Keep an
		// active destination and the real local link record pointing at it.
		execTest(t, f.service.Store, "INSERT INTO session_links(database_id,session_id,workspace_id,local_workspace_id,remote,credential_file,name) VALUES(?,?,?,?,?,?,?)", f.service.Store.id, f.sessions[0], f.workspace, f.workspace, "matrix", filepath.Join(f.path, "friend.json"), "Linked")
	}

	progress, archived := task, ""
	if task == "archived" {
		progress, archived = "finished", now()
	}

	execTest(t, f.service.Store, "UPDATE tasks SET progress=?,archived_at=? WHERE id=?", progress, archived, f.task)
}

func matrixOutcome(t *testing.T, outcomes string, index int) string {
	t.Helper()

	values := strings.Fields(outcomes)
	if len(values) == 1 {
		return values[0]
	}

	if len(values) != 5 {
		t.Fatal("state row must have a default or five cells", outcomes)
	}

	return values[index]
}

func TestStateCommandMatrix(t *testing.T) {
	commands := matrixCommands(t)
	rows := map[string]stateMatrixRow{}
	for _, row := range stateMatrix {
		for _, command := range strings.Split(row.commands, "|") {
			if _, ok := rows[command]; ok {
				t.Fatal("duplicate command", command)
			}

			rows[command] = row
		}
	}

	for _, command := range matrixLocalCommands {
		rows[command] = stateMatrixRow{caller: "local"}
	}

	for _, command := range commands {
		if _, ok := rows[command]; !ok {
			t.Errorf("unlisted command %q: add state matrix row", command)
		}
	}

	for command := range rows {
		if !slices.Contains(commands, command) {
			t.Errorf("matrix command no longer in table: %q", command)
		}
	}

	if t.Failed() {
		t.FailNow()
	}

	f := newMatrixFixture(t)
	matrixStateInventory(t, f)

	for _, command := range commands {
		row := rows[command]
		if row.caller == "local" {
			continue
		}

		for axis, states := range [][]string{matrixSessionStates, matrixTaskStates} {
			for i, state := range states {
				t.Run(command+"/"+[]string{"session", "task"}[axis]+"/"+state, func(t *testing.T) {
					f.reset(t)

					session, task := "open", "waiting"
					outcomes := row.sessions
					if axis == 0 {
						session = state
					} else {
						task = state
						outcomes = row.tasks
					}

					f.state(t, session, task)

					got, _ := f.call(t, "/v1/"+row.route, row.caller)
					if want := matrixOutcome(t, outcomes, i); got != want {
						t.Errorf("want %s, got %s", want, got)
					}
				})
			}
		}
	}
}
