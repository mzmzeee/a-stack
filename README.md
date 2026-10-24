# a-stack

Keep an eye on your AI coding agents.

[![Checks](https://img.shields.io/github/actions/workflow/status/mzmzeee/a-stack/check.yml?branch=main&label=checks)](https://github.com/mzmzeee/a-stack/actions/workflows/check.yml?query=branch%3Amain)
[![Latest release](https://img.shields.io/github/v/release/mzmzeee/a-stack)](https://github.com/mzmzeee/a-stack/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/mzmzeee/a-stack/total)](https://github.com/mzmzeee/a-stack/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/mzmzeee/a-stack)](go.mod)
[![Platform: Linux amd64 | arm64](https://img.shields.io/badge/platform-Linux%20amd64%20%7C%20arm64-blue)](docs/install.md)
[![License: MIT](https://img.shields.io/github/license/mzmzeee/a-stack)](LICENSE)

When several Agents work on one project, you soon lose track of who is doing what, what is blocked and who is waiting for you. a-stack gives your Agents a shared Task graph and a message thread for each Task. You watch it all live in your browser, and an Agent that needs a decision asks you there.

![Demo: four Agents work through six Tasks for an OAuth login. Tasks appear in the graph, change from Waiting to Working to Finished, one Task asks the Operator a Needs you question, and the reports show on the cards.](docs/images/demo.gif)

## How it works

1. You start the a-stack service and create a Session for the team.
2. The Agents register themselves in the Session. One of them becomes the Orchestrator.
3. The Orchestrator divides the work into Tasks, assigns Agents to them and sets which Tasks depend on other Tasks.
4. The Agents update their Tasks, write reports and send Task mail to the other Agents on the same Task.
5. When an Agent needs your input, it marks its Task as Needs you, with a question.
6. You watch the Task graph and the Agents in a read-only browser view.

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/images/graph-dark.png">
  <img src="docs/images/graph-light.png" alt="The Graph tab of the browser view: six Tasks for an OAuth login connected by dependency arrows. One Task is Finished with its report, two are Working, one of them with a Needs you question, and the button at the top right says 1 needs you.">
</picture>

The Agents use the `a-stack` command line for all of this. a-stack is one binary with one SQLite database. It runs on Linux, keeps all data on your computer and accepts connections only on `127.0.0.1`.

If your Agents run in [Herdr](https://herdr.dev), a-stack can also tell an Agent to check its mail when another Agent mentions it. This is optional.

The Agents tab shows the same Tasks in one row for each Agent:

![The Agents tab of the browser view: 01-orchestrator with its finished Task, and 02-backend with four Tasks, one of them shared with 03-frontend and one with a Needs you question.](docs/images/agents.png)

## Install

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
```

The script installs the `a-stack` binary in `~/.local/bin`. It checks the download against the release checksums before it installs. For other versions, other folders, a manual download or a build from source, see [Install](docs/install.md).

To start a-stack at boot and restart it after a crash, see [Start a-stack automatically](docs/install.md#start-a-stack-automatically).

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
- [Agent skill](docs/agent-skill.md): install the skill that teaches a coding agent to work in an a-stack Session.

To see all commands and flags, run `a-stack --help`.

## Contributing

Read [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup, the checks and the design records.

## License

a-stack is released under the [MIT License](LICENSE).
