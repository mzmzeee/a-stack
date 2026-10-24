# a-stack

A local coordination service for a team of AI coding agents, with a read-only browser view for the person supervising them.

The Agents split the work into Tasks, record which Tasks depend on which, write reports and message each other through the `a-stack` command line. You watch the Task graph live in your browser and answer when an Agent asks you a question.

a-stack is one Go binary with one SQLite database. It accepts connections only on `127.0.0.1`. Version 1 runs on Linux only.

## Features

- One Agent, the Orchestrator, creates Tasks, assigns Agents to them and adds dependencies. The other Agents update the Tasks they are assigned to.
- The Agents on a Task share one message thread, the Task mail. A mention (`--mention NAME`) points a message at a specific Agent.
- An Agent can mark a Task as Needs you, with a question for you. The browser view shows which Tasks are waiting on you.
- The browser view has Graph and Agents tabs, light and dark themes, and updates while you watch. Nothing in it can change the work.
- If your Agents run in Herdr, a-stack can tell an Agent's terminal to check its mail when someone mentions it. This is optional.
- Each Agent has its own private credential. Repeating a change with the same operation ID applies it once, so retries are safe.
- You can back up the database while the service runs, and restore it into a new state directory.

## Install

a-stack runs on Linux, on x86-64 and ARM64. Install the latest release into `~/.local/bin`:

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
```

The script checks the download against the release's `SHA256SUMS` before it installs anything. To install a specific version or into another directory, set `A_STACK_VERSION` or `A_STACK_INSTALL_DIR`:

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | A_STACK_VERSION=v0.1.0 A_STACK_INSTALL_DIR="$HOME/bin" sh
```

To install by hand, download `a-stack_linux_amd64.tar.gz` or `a-stack_linux_arm64.tar.gz` from the [releases page](https://github.com/mzmzeee/a-stack/releases), compare it with `SHA256SUMS`, and unpack the `a-stack` binary onto your `PATH`. The release binaries are statically linked and need no other libraries.

Check the installed version:

```sh
a-stack version
```

### Requirements

You need these only to build from source:

- Go 1.27.3 with cgo (`CGO_ENABLED=1`) and a C compiler

SQLite 3.53.4 is built into the binary from `github.com/mattn/go-sqlite3`. Do not build with the `libsqlite3` tag.

### Build from source

```sh
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
```

## Quick start

Start the service in one terminal. It stays in the foreground until you press Ctrl-C:

```sh
a-stack serve
```

```json
{"data":{"database_id":"eD1K…","status":"ready","url":"http://127.0.0.1:41541"}}
```

On the first start, a-stack creates its state directory at `~/.local/state/a-stack` (or `$XDG_STATE_HOME/a-stack`) and saves your Operator credential there. Later commands use that credential automatically.

In a second terminal, create a Workspace for your project folder and a Session for the team:

```sh
a-stack workspace create --path ~/code/my-project --operation-id create-my-project
```

```json
{"data":{"operation_id":"create-my-project","resource_id":"_P8a…","revision":1,"outcome":"created",…}}
```

Use the `resource_id` from the output as the Workspace ID. `session create` prints the Session ID the same way:

```sh
a-stack session create --workspace WORKSPACE_ID --name Build --operation-id create-build-session
a-stack open --workspace WORKSPACE_ID --session SESSION_ID
```

`open` opens the Session in your browser. The Task graph is empty until you register Agents and they start creating Tasks.

Every command that changes something needs an `--operation-id` that you choose: 8 to 128 letters, digits, `_` or `-`. If a command fails or you are not sure it ran, run it again with the same ID. a-stack applies the change only once.

Next, follow [Getting started](docs/getting-started.md) to register Agents, appoint an Orchestrator and run a complete Task from start to finish.

## Documentation

- [Getting started](docs/getting-started.md): a complete walkthrough with a three-Agent team, from setup to backup and restore.
- [Workspaces, Sessions and Agents](docs/lifecycle.md): register Agents, recover credentials, close and reopen Sessions.
- [Tasks and the Task graph](docs/tasks.md): create and assign Tasks, report progress, ask questions, add dependencies.
- [Task mail](docs/mail.md): send and read messages, mentions and limits.
- [Browser view](docs/browser.md): what the view shows and how to open it.
- [Mention notices](docs/notifications.md): set up Herdr notices and fix delivery problems.
- [Local service](docs/foundation.md): the state directory, credentials, command output and exit codes, the HTTP API, backup and restore.

For every command and flag, run `a-stack --help`.

## Contributing

Run all checks before you open a pull request:

```sh
scripts/check.sh
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, the issue tracker and the design records.
