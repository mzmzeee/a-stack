# a-stack

a-stack helps you follow the work of several AI coding agents on one Linux computer. In a-stack, you are the Operator: the person who supervises the Agents and answers when they need you.

The Agents report their work and coordinate through the `a-stack` command line. You watch the work in a read-only browser view. The work is split into Tasks. A Task is a named piece of work with one shared status, a report and Task mail. The Task graph is a map of the Tasks and the dependencies between them.

These terms appear in every guide:

- A Workspace is one project folder.
- A Session is one team inside a Workspace. Each Session has its own Task graph.
- An Agent is an AI assistant that is registered in one Session.
- The Orchestrator is the Agent that coordinates a Session. You appoint it. It creates Tasks, assigns Agents to them and adds dependencies.
- Task mail is the messages that the Agents assigned to a Task send to each other. All assigned Agents see the same messages.
- Needs you marks a Task where an Agent asks you a clear question.

The [glossary](GLOSSARY.md) defines all terms.

a-stack is one Go binary. The binary contains the command line, a local service and the browser view. The service keeps all data in one SQLite database and accepts connections only on `127.0.0.1`. An optional Herdr adapter can tell an Agent to check its mail when another Agent mentions it with `@name`.

## Requirements

- Linux (the first supported platform).
- Go 1.27.3. The `.go-version` and `go.mod` files set this version.
- cgo (`CGO_ENABLED=1`) and a C compiler. The project is tested with GCC 16.2.1.
- Python 3, only for the JSON helper in the getting-started guide and for the validation runner.

The only dependency is `github.com/mattn/go-sqlite3` v1.14.52. It includes SQLite 3.53.4. Do not build with the `libsqlite3` build tag. The service does not start if SQLite is older than 3.51.3.

## Build

Run this command in the repository root:

```sh
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
```

## Quick start

Start the service:

```sh
./a-stack serve --json
```

The service stays in the foreground. When it is ready, it prints one JSON line with its local URL and database ID. To stop the service, press Ctrl-C.

Open a second terminal in the same folder and check the service:

```sh
./a-stack health --json
./a-stack storage status --json
```

`health` shows the database ID, the SQLite version and the schema version. `storage status` shows the sizes of the database files.

On the first start, the service makes the Operator credential `operator.json` in the state directory. Commands use this credential when you do not give a `--credential-file`.

The state directory is `$XDG_STATE_HOME/a-stack`. If `XDG_STATE_HOME` is not set, it is `~/.local/state/a-stack`. To use a different directory, give the same absolute `--state-dir DIR` to every command. The directory must be outside all Git checkouts.

Every command except `open` and `--help` prints a JSON `data` object on stdout, also without `--json`. If a command fails, it prints a JSON `error` object on stderr and nothing on stdout. The exit code is 0 for success, 2 when the error code is `invalid_input`, and 1 for all other errors.

To list all commands and flags, run `./a-stack --help`.

## Documentation

- [Getting started](docs/getting-started.md): a complete team workflow with disposable state, from enrollment to backup and restore.
- [Local service](docs/foundation.md): the state directory, credentials, HTTP rules, SQLite settings, and backup and restore.
- [Workspaces, Sessions and Agents](docs/lifecycle.md): enrollment, credential recovery, runtime bindings, and Session close and reopen.
- [Tasks and the Task graph](docs/tasks.md): Task commands, progress, Needs you questions, dependencies and the read API.
- [Task mail](docs/mail.md): send, read and check mail, cursors, mentions and limits.
- [Browser view](docs/browser.md): the read-only Session view and its measured limits.
- [Mention notices](docs/notifications.md): optional Herdr notices, setup, diagnostics and recovery.
- [Validation](docs/validation.md): the v1 validation runner and its measured results.
- [Quality checks](docs/quality.md): the linters, scanners and tests that `scripts/check.sh` runs, and their limits.

The decision records explain why a-stack works the way it does:

- [0001: Orchestrator authority and Task rules](docs/adr/0001-graph-only-approval.md)
- [0002: Local Go service and SQLite](docs/adr/0002-local-service-and-sqlite.md)
- [0003: Retained shared Task mail](docs/adr/0003-retained-task-mail.md)
- [0004: Herdr mention notices](docs/adr/0004-herdr-notifications.md)
- [0005: Read-only browser](docs/adr/0005-read-only-browser.md)

The [v1 specification](https://github.com/mzmzeee/a-stack/issues/1) and its tickets are GitHub issues. The [approved visual prototype](prototypes/h-stack-visual/README.md) is the design reference for the browser view, kept separate from the production code.

The research notes on [agent coordination](docs/research/agent-coordination.md), [agent mail systems](docs/research/agent-mail-systems.md) and [Herdr integration](docs/research/herdr-integration.md) record the evidence and its limits. Their earlier proposals do not override the specification or the decision records.

## Development

Run the tests:

```sh
go test ./...
go test -race ./...
```

The tests use temporary state, real SQLite and HTTP, and CLI subprocesses. They do not install anything, and they do not send anything to existing agent terminals.

To run every check that CI runs (formatting, linters, vulnerability and secret scans, tests with coverage, race tests, build and the quick validation), use:

```sh
scripts/check.sh
```

[Quality checks](docs/quality.md) lists the tool versions and prerequisites. To run the full v1 validation runner with measurements, see [Validation](docs/validation.md).
