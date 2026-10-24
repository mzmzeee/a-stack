# a-stack

Keep an eye on your AI coding agents.

When several Agents work on one project, you soon lose track of who is doing what, what is blocked and who is waiting for you. a-stack gives your Agents a shared Task graph and a message thread for each Task. You watch it all live in your browser, and an Agent that needs a decision asks you there.

## How it works

1. You are the Operator. You start the a-stack service and create a Session for the team.
2. You register the Agents and appoint one of them as the Orchestrator.
3. The Orchestrator divides the work into Tasks, assigns Agents to them and sets which Tasks depend on other Tasks.
4. The Agents update their Tasks, write reports and send Task mail to the other Agents on the same Task.
5. When an Agent needs your input, it marks its Task as Needs you, with a question.
6. You watch the Task graph and the Agents in a read-only browser view.

The Agents use the `a-stack` command line for all of this. a-stack is one binary with one SQLite database. It runs on Linux, keeps all data on your computer and accepts connections only on `127.0.0.1`.

If your Agents run in [Herdr](https://herdr.dev), a-stack can also tell an Agent to check its mail when another Agent mentions it. This is optional.

## Install

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
```

The script installs the `a-stack` binary in `~/.local/bin`. It checks the download against the release checksums before it installs. For other versions, other folders, a manual download or a build from source, see [Install](docs/install.md).

## Quick start

1. Start the service, and keep this terminal open:

   ```sh
   a-stack serve
   ```

2. In a second terminal, create a Workspace for your project folder:

   ```sh
   a-stack workspace create --path ~/code/my-project --operation-id create-my-project
   ```

   ```json
   {"data":{"operation_id":"create-my-project","resource_id":"_P8auKrcy…","revision":1,"outcome":"created",…}}
   ```

   The `resource_id` is the Workspace ID.

3. Create a Session in the Workspace. The `resource_id` in the output is the Session ID.

   ```sh
   a-stack session create --workspace WORKSPACE_ID --name Build --operation-id create-build-session
   ```

4. Open the Session in your browser:

   ```sh
   a-stack open --workspace WORKSPACE_ID --session SESSION_ID
   ```

The Task graph is empty at this point. To add Agents and Tasks, follow [Getting started](docs/getting-started.md).

Each command that changes data needs an `--operation-id`. You choose the ID: 8 to 128 letters, digits, `_` or `-`. If you are not sure that a command ran, run it again with the same ID. a-stack makes the change only one time.

## Documentation

- [Getting started](docs/getting-started.md): set up a team of three Agents and take one Task from start to finish.
- [Workspaces, Sessions and Agents](docs/lifecycle.md): register Agents, recover credentials, close and reopen Sessions.
- [Tasks and the Task graph](docs/tasks.md): create and assign Tasks, report progress, ask questions, add dependencies.
- [Task mail](docs/mail.md): send and read messages, and mention Agents.
- [Browser view](docs/browser.md): open the view and read what it shows.
- [Mention notices](docs/notifications.md): set up Herdr notices and fix delivery problems.
- [Local service](docs/foundation.md): the state directory, credentials, command output, the HTTP API, backup and restore.

To see all commands and flags, run `a-stack --help`.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, the checks and the design records.
