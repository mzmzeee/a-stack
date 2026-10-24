# a-stack

a-stack helps an Operator follow local coding agents through a shared Task graph, reports and Task mail. Agents work through the CLI; the browser is a read-only view.

Follow the [Operator and Agent getting-started guide](docs/getting-started.md) for a disposable team workflow, private credentials, shared mail and recovery.

## Implementation

The [v1 specification and implementation backlog](https://github.com/mzmzeee/a-stack/issues/1) define the first release. Start with [the local service and storage foundation](https://github.com/mzmzeee/a-stack/issues/2); later tickets have explicit dependencies and acceptance criteria. GitHub Issues are the source of truth for specs and tickets.

The selected implementation is one Go binary, SQLite storage, embedded HTML/CSS/JavaScript/SVG, and an optional Herdr adapter for @mention notices. The service/SQLite/CLI foundation, Workspace/Session/Agent lifecycle, shared Tasks, dependency graph, retained Task mail and read-only browser are implemented. The optional notice dispatcher uses only explicitly verified Herdr/harness combinations.

## Run the foundation

Linux is supported first. Build with **Go 1.27.3**, `CGO_ENABLED=1` and a C compiler (tested with GCC 16.2.1). `.go-version` and `go.mod` pin the toolchain baseline. The only dependency is `github.com/mattn/go-sqlite3` **v1.14.52**, using its bundled SQLite **3.53.4**; do not use the `libsqlite3` build tag. Startup also enforces SQLite >=3.51.3.

```sh
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
./a-stack serve --json
```

`serve` stays in the foreground; stop it with Ctrl-C. In another terminal:

```sh
./a-stack health --json
./a-stack storage status --json
backup_dir="$(mktemp -d)"
./a-stack storage backup --destination "$backup_dir/a-stack.sqlite" --json
```

State defaults to `$XDG_STATE_HOME/a-stack` or `~/.local/state/a-stack`. Use the same absolute `--state-dir DIR` on every command to select another state directory outside project checkouts. Existing state directories must be private (0700); files are 0600. Clients only read discovery/credentials and use HTTP; they never start a service or open SQLite.

All foundation commands emit a JSON `data` envelope on stdout, including without `--json`. Errors emit a JSON `error` envelope on stderr, leaving stdout empty; exit codes are 0 success, 2 invalid CLI input, 1 other errors. Explicit `--credential-file FILE` selects a private credential; `--as agent` and `--as browser` require that flag and reject an Operator file. They never fall back to the Operator credential.

```sh
go test ./...
go test -race ./...
```

Tests use temporary state, real SQLite/HTTP, and disposable CLI subprocesses. Nothing is installed or launched against existing agent terminals. See [foundation contracts and restore procedure](docs/foundation.md) for backup recovery and the interfaces used by later slices.

See [lifecycle enrollment and authority](docs/lifecycle.md) for `workspace`, `session` and `agent` commands, durable credential delivery, runtime attachment and revision-checked close/reopen. Every Agent command uses an explicit Workspace/Session; changing cwd does not select context.

See [shared Tasks, graph and bounded snapshot API](docs/tasks.md) for multi-assignee updates, attention, retained history and the mail/browser handoff.

See [retained Task mail](docs/mail.md) for send/read/check, caller-held cursors, mentions, storage measurements and the notification handoff.

See [the read-only browser](docs/browser.md) for `open`, scoped credentials, polling, keyboard controls and measured layout limits.

See [Herdr mention notices](docs/notifications.md) for opt-in setup, verified versions, durable batching, diagnostics and uncertain-outcome recovery.

See [reproducible v1 validation and measured limits](docs/validation.md) for the disposable workflow, recovery checks, timings and storage growth.

## Domain decisions

- [Glossary](GLOSSARY.md)
- [0001: Orchestrator authority and Task rules](docs/adr/0001-graph-only-approval.md)
- [0002: Local Go service and SQLite](docs/adr/0002-local-service-and-sqlite.md)
- [0003: Retained shared Task mail](docs/adr/0003-retained-task-mail.md)
- [0004: Herdr mention notifications](docs/adr/0004-herdr-notifications.md)
- [0005: Read-only browser](docs/adr/0005-read-only-browser.md)

The [approved visual prototype](prototypes/h-stack-visual/README.md) is the design baseline, preserved separately from production code. The [coordination research](docs/research/agent-coordination.md), [mail review](docs/research/agent-mail-systems.md) and [Herdr research](docs/research/herdr-integration.md) explain the evidence and its limits; their earlier proposals do not override the current spec or ADRs.
