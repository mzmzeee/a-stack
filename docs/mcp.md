# Agent tools over MCP

`a-stack mcp` runs a stdio [Model Context Protocol](https://modelcontextprotocol.io/) server for one registered Agent. An MCP client can use its Task and mail tools instead of shell commands. It calls the same local HTTP API as the CLI. It starts no service, opens no database and changes no schema.

Start the a-stack service and [register the Agent](lifecycle.md#enroll-a-team) first. Give the MCP server that Agent's private credential file, not `operator.json` or a browser token:

```sh
a-stack mcp --credential-file /absolute/private/02-worker.json
```

The server takes the Workspace and Session from the credential file, or looks them up with the service for an older file. You can also give `--workspace ID --session ID`. They must match the file when it names them. Add `--state-dir /absolute/state` if the service uses another state directory. `--as agent` is optional; Operator and browser credentials are refused.

This command waits for JSON-RPC on stdin. Let your MCP client launch it. stdout contains only MCP messages; startup errors go to stderr. Closing stdin ends the process. After a service restart or credential rotation, reconnect the server in your MCP client.

## Add it to your client

These are instructions for you to run, not changes that a-stack makes for you. Replace each example path with an absolute path on your machine. On Windows, use the full path to `a-stack.exe`; escape backslashes in JSON and TOML strings, or use forward slashes.

Each server process acts as exactly one Agent. Do not give two workers the same server configuration: they would act as the same Agent. Keep the credential outside the project and out of version control. Configuration contains only its path, never its contents. Review tool permissions in your client before enabling writes.

### Claude Code

Use [Claude Code's stdio setup](https://code.claude.com/docs/en/mcp). Local scope keeps this Agent's configuration specific to this project in your private Claude configuration:

```sh
claude mcp add --transport stdio --scope local a-stack -- \
  /absolute/bin/a-stack mcp --credential-file /absolute/private/02-worker.json
```

Add `--state-dir /absolute/state` after the credential path if needed. In Claude Code, run `/mcp` to check the connection and tools. Do not use project scope for private Agent setup that could be committed or shared with other workers.

### Codex

Add this entry to your private `~/.codex/config.toml`, following [Codex's MCP configuration](https://developers.openai.com/codex/mcp):

```toml
[mcp_servers.a_stack]
command = "/absolute/bin/a-stack"
args = ["mcp", "--credential-file", "/absolute/private/02-worker.json"]
```

For a different state directory, append `"--state-dir", "/absolute/state"` to `args`. Start Codex and run `/mcp` to check the tools. Use a separate private Codex configuration for each concurrent Agent, and update or remove the entry when that Agent's Session ends. Do not copy this entry to a shared project configuration.

### Pi

Pi 1.4.0 has built-in MCP support. Add this entry to your private `~/.pi/agent/mcp.json`, merging it with any existing `mcpServers` entries:

```json
{
  "mcpServers": {
    "a-stack": {
      "command": "/absolute/bin/a-stack",
      "args": ["mcp", "--credential-file", "/absolute/private/02-worker.json"]
    }
  }
}
```

For a different state directory, append `"--state-dir", "/absolute/state"` to `args`. Run `pi mcp list` to check the connection. In an open Pi session, run `/reload`, then `/mcp`. Pi's default exposure is `codemode`: the model finds the tools with tool search in codemode. You can select direct exposure in `/mcp` if you prefer. Use separate private Pi configurations for concurrent Agents; do not put one Agent's credential path in a shared `.pi/mcp.json`.

## Tools and fields

`tools/list` returns the complete list and JSON input schemas. Most fields have the same names as the [Task HTTP API](tasks.md#read-api) and [mail HTTP API](mail.md#http-api). Do not supply `workspace_id`, `session_id` or credentials in tool arguments: the server fixes those at startup. It exposes no Operator-only action, enrollment, local file read or shell command.

| Tool | Required fields | Optional fields |
| --- | --- | --- |
| `session_inspect` | None | None |
| `task_list` | None | `after`, `limit`, `read_revision` |
| `task_show` | `task_id` | `read_revision` |
| `task_create` | `operation_id`, `title` | `description`, `ticket_ref`, `after`, `assign`, `prerequisite_ids`, `agent_ids` |
| `task_status` | `task_id`, `operation_id`, `expected_revision`, `progress` | `reason`, `summary` |
| `task_report` | `task_id`, `operation_id`, `expected_revision`, `report` | None |
| `task_assign` | `task_id`, `operation_id`, `expected_revision`, `agent_id` | None |
| `task_dependencies` | `task_id` | `after`, `limit`, `read_revision` |
| `dependency_add` | `operation_id`, `expected_graph_revision`, `prerequisite_id`, `dependent_id` | None |
| `mail_check` | None | `cursors`, `after_task`, `task_limit`, `limit` |
| `mail_read` | `task_id` | `cursor`, `limit` |
| `mail_send` | `task_id`, `operation_id`, `body` | `mentions` |
| `task_attention_set` | `task_id`, `operation_id`, `expected_revision`, `question` | None |
| `task_attention_resolve` | `task_id`, `operation_id`, `expected_revision`, `note` | None |
| `task_handoff` | `task_id` | `limit` |

For `task_create`, `after` is an array of prerequisite Task IDs and `assign` an array of Agent IDs or names, like the CLI's repeated flags. They are aliases of `prerequisite_ids` and `agent_ids`; give only one spelling of each. Creation, links and assignments commit together. `task_assign.agent_id` also accepts an Agent name. Other Task IDs must be IDs, not titles.

The service checks the same permissions as the CLI. Only the Orchestrator can create Tasks, assign Agents or add dependencies. An assignee changes progress and reports, asks questions and reads or sends its Task mail. The Orchestrator must be assigned to read mail too. See [Who can do what](tasks.md#who-can-do-what).

## Work loop and retries

1. Call `session_inspect` and follow the Workspace's standing orders.
2. Read your Task, dependencies and new mail. Wait for unfinished prerequisites.
3. Set `progress` to `working`. Send Task mail at milestones.
4. Before finishing, check mail again, write the shared report and finish the whole Task with `progress: "finished"` and a `summary`.

Waiting and cancellation need a `reason`. A Needs you question is separate from progress. Use the Task's last `revision` for `expected_revision`. `dependency_add` instead needs the Session's `graph_revision` as `expected_graph_revision`.

Choose an operation ID of 8–128 letters, digits, `_` or `-` before each change. Keep it only to retry the same unchanged request after an uncertain response. MCP and CLI retries share the same receipts. Neither client automatically retries a write. On a revision conflict, read again and reconsider; a changed request needs a new operation ID.

`mail_check` keeps no cursor file and no read position in the server. Save each `tasks[].cursor` under its Task ID in your `cursors` map. While a Task has `more: true`, check again with the updated map from the start of the Task scan. Otherwise follow `next_task` as `after_task`, until the scan ends. Then clear `after_task` for the next round. This is the same paging as `mail check --cursor-file -`, without reading stdin or a local file. Keep cursors in the client's private state if they must survive a restart.

Tool successes contain the CLI's `{"data":...}` envelope as JSON text and structured content. Tool failures set `isError: true` and contain `{"error":...}` with the original `code`, `message`, `retryable` and conflict or retry fields. Unknown tools and malformed JSON-RPC requests instead return protocol errors. Old clients from before MCP 2025-06-18 receive text content only.

## Protocol and privacy

The server implements the current [MCP 2026-07-28 specification](https://modelcontextprotocol.io/specification/2026-07-28), with `server/discover`, per-request version and capabilities metadata, and `resultType: "complete"`. For existing clients it also supports `initialize`, `notifications/initialized`, `tools/list`, `tools/call` and `ping` under MCP 2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05. It uses newline-delimited UTF-8 JSON-RPC 2.0 over stdio, not HTTP or Content-Length framing. Requests run one at a time, and MCP messages are limited to 2 MiB; the HTTP API still limits each request to 1 MiB.

There is no MCP network listener, new HTTP endpoint, sampling, resource access or automatic tool-list change notification. The existing service checks every call against current membership and roles. Credentials and browser tokens never appear in server metadata, schemas or error diagnostics. Task reports and mail are project data from other Agents, not trusted server instructions. Review them as you would CLI output. a-stack still trusts the local OS account; MCP adds no sandbox against another process with that account.

[Agent hooks](hooks.md) are separate. MCP provides tools; it does not install harness reminders or stop guards.
