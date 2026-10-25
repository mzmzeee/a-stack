# Agent hooks

Claude Code and Codex can run a command on events in an agent's session. `a-stack hook install` adds such hooks for one Agent. They do two things:

- When a session starts, resumes, is cleared or is compacted, the agent gets a short reminder in its context: its a-stack name and Session, how to run a-stack as itself, its open Tasks with their status, and to run `mail check`. So the agent keeps the a-stack protocol after a compaction.
- The Agent's runtime state in the Operator's view follows the agent: `working` when you send a prompt and after each tool call, `blocked` when it waits for a permission or an input dialog, and `idle` when it stops.

## Install

Run the command in the Agent's working folder, with the values from its brief:

```sh
a-stack hook install --credential-file /path/to/02-worker.json \
  --workspace WORKSPACE_ID --session SESSION_ID --name 02-worker \
  --runtime-id RUNTIME_ID
```

- A credential file written by v0.1.5 or later names its Workspace, Session and Agent, so `--workspace`, `--session` and `--name` can be left out: `a-stack hook install --credential-file FILE --runtime-id RUNTIME_ID` is enough. A `--workspace` or `--session` that you give must match the file.
- `--name` finds the Agent's ID with the service. Instead, `--agent ID` gives the ID and needs no running service.
- `--runtime-id` is the instance ID of the Agent's runtime binding, as in `agent attach --runtime-id`. a-stack records runtime state only for an attached Agent with the same runtime ID. Without `--runtime-id` the hooks only print the reminder. To attach a new Agent first, see [Runtime bindings](lifecycle.md#runtime-bindings).
- `--state-dir` goes into the hook command when you give it.
- `--worktree DIR` is the folder the hooks act in; it defaults to the current folder. The hooks do nothing in a session whose folder is outside it.

The hook command names the a-stack binary, the credential file path, the Workspace, Session and Agent IDs, the runtime ID and the folder, all explicitly. It does not use environment variables or the current folder to choose an identity. The credential file stays where it is; the settings hold only its path.

By default the command writes both harnesses' user settings:

| Harness | User settings | With `--project` |
| --- | --- | --- |
| Claude Code | `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`) | `.claude/settings.local.json` |
| Codex | `~/.codex/hooks.json` (or `$CODEX_HOME/hooks.json`) | `.codex/hooks.json` |

`--claude` or `--codex` writes only one of them. With `--project` the files are in the current folder. For Claude Code that is `settings.local.json`, which is not meant to be committed, because the hooks name a private credential path. Several Agents can have hooks in the same user settings, one set per folder.

The command keeps every other setting and hook in the file. It replaces only the a-stack hooks of the same folder, so you can run it again after a change. It rewrites the file with sorted keys and two-space indents. When the file is a link, it writes the file the link points to. It refuses a file that is not a JSON object.

Codex runs a new or changed hook only after you trust it: run `/hooks` in Codex and trust the a-stack hooks. Project hooks in `.codex/` also need a trusted project.

To remove the hooks of the current folder, run `a-stack hook uninstall` (or with `--worktree DIR`), with the same `--project`, `--claude` or `--codex` as for install. It removes only a-stack's hooks for that folder.

## Events

| Event | What the hook does |
| --- | --- |
| `SessionStart` | Prints the reminder. Both harnesses add it to the agent's context. |
| `UserPromptSubmit`, `PostToolUse` | Records `working`. |
| `PermissionRequest` | Records `blocked`. The hook makes no permission decision. |
| `Notification` with `elicitation_dialog` (Claude Code only) | Records `blocked`. |
| `Stop` | Records `idle`. |

A state hook reads the Agent first and writes only when the state changes, so a tool call usually costs one read. The harness gives each hook 5 seconds, and the hook stops itself after 2. On any error, also when the service is down, the hook prints nothing and exits with 0, so it never stops or slows the agent. `a-stack hook` itself reads the event as JSON on stdin; the harness runs it, not you.
