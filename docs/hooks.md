# Agent hooks

Claude Code and Codex can run a command on events in an agent's session. `a-stack hook install` adds such hooks for one Agent. They do four things:

- When a session starts, resumes, is cleared or is compacted, the agent gets a short reminder in its context: its a-stack name and Session, how to run a-stack as itself, its open Tasks with their status, and to run `mail check`. So the agent keeps the a-stack protocol after a compaction.
- The Agent's runtime state in the Operator's view follows the agent: `working` when you send a prompt and after each tool call, `blocked` when it waits for a permission or an input dialog, and `idle` when it stops.
- The [Stop guard](#stop-guard) keeps the agent from stopping once while a Task that it set `working` has no report.
- The [Task mail nudge](#task-mail-nudge) tells the agent when another assignee wrote Task mail, at most every 30 seconds.

## Install

Run the command in the Agent's working folder, with the values from its brief:

```sh
a-stack hook install --credential-file /path/to/02-worker.json \
  --workspace WORKSPACE_ID --session SESSION_ID --name 02-worker \
  --runtime-id RUNTIME_ID
```

- A credential file written by v0.1.5 or later names its Workspace, Session and Agent, so `--workspace`, `--session` and `--name` can be left out: `a-stack hook install --credential-file FILE --runtime-id RUNTIME_ID` is enough. A `--workspace` or `--session` that you give must match the file.
- `--name` finds the Agent's ID with the service. Instead, `--agent ID` gives the ID and needs no running service.
- `--runtime-id` is the instance ID of the Agent's runtime binding, as in `agent attach --runtime-id`. a-stack records runtime state only for an attached Agent with the same runtime ID. Without `--runtime-id`, the hooks record no runtime state. The reminder, the Stop guard and the Task mail nudge still work. To attach a new Agent first, see [Runtime bindings](lifecycle.md#runtime-bindings).
- `--state-dir` goes into the hook command when you give it.
- `--worktree DIR` is the folder the hooks act in; it defaults to the current folder. The hooks do nothing in a session whose folder is outside it.
- `--no-stop-guard` and `--no-mail-nudge` turn off the Stop guard and the Task mail nudge. Both are on by default. Run install again without them to turn them on.

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
| `SessionStart` | Prints the reminder as `hookSpecificOutput.additionalContext` JSON. Both harnesses add it to the agent's context. Starts the Task mail nudge's cursor file. |
| `UserPromptSubmit`, `PostToolUse` | Records `working`. Runs the Task mail nudge. |
| `PermissionRequest` | Records `blocked`. The hook makes no permission decision. |
| `Notification` with `elicitation_dialog` (Claude Code only) | Records `blocked`. |
| `Stop` | Runs the Stop guard. When the guard does not block, records `idle`. |

A state hook reads the Agent first and writes only when the state changes, so a tool call usually costs one read. The harness gives each hook 5 seconds, and the hook stops itself after 2. On any error, also when the service is down, the hook prints nothing and exits with 0, so it never stops or slows the agent. `a-stack hook` itself reads the event as JSON on stdin; the harness runs it, not you.

## Stop guard

When the agent stops, the hook reads the Agent's Tasks. It blocks the stop when one of them is `working`, this Agent set it to `working`, it does not wait for the Operator's answer, and it has no report written since. A report written earlier, before the Task was set to `working` again, does not count. The other assignees of the Task, such as a helper or an Orchestrator that follows it, can stop. In a closed Session the guard never blocks.

The agent gets a reason that names the Task and gives the ways out as whole commands, with the Task's revision and operation IDs, and goes on:

```text
a-stack: your Task "Build the parser" (Task TASK_ID) is still working and has no report since you set it working. Before you stop, do one of these, with your own text:
1. Report and finish:
a-stack --as agent --credential-file '/path/to/02-worker.json' --workspace WORKSPACE_ID --session SESSION_ID task report --task TASK_ID --report 'What you did, and where' --expected-revision 5 --operation-id guard-TASK_ID_ST-5-report
a-stack --as agent … task status --task TASK_ID --progress finished --summary 'What you did' --expected-revision 6 --operation-id guard-TASK_ID_ST-6-finish
2. Wait:
a-stack --as agent … task status --task TASK_ID --progress waiting --reason 'What it waits for' --expected-revision 5 --operation-id guard-TASK_ID_ST-5-wait
```

When other Agents share the Task, the reason adds: read its report with `task show` before you replace it, and if only your part is done, say so with `mail send` and leave the Task working.

The guard blocks at most once in a row. When the agent stops again right after, the harness sets `stop_hook_active`. The hook also leaves a marker file next to the [mail nudge's cursor file](#task-mail-nudge), `HASH.stop`, so that the next stop passes also in a harness that does not send the flag. The guard lets the agent stop when the service is down, a read fails or the 2 seconds run out. A blocked stop records no `idle`.

The hook prints `{"decision":"block","reason":"..."}`, the same for Claude Code and Codex. Claude Code shows the reason as Stop hook feedback; Codex continues the turn with the reason as a new prompt.

The guard costs two reads at each stop: the assignments and the Task list. A working Task whose latest change is not its status change also costs a read of its history, to find who set it working. A block costs one more read, to check that the Session is open.

## Task mail nudge

After a tool call or a prompt, the hook checks the Agent's Task mail, at most once every 30 seconds per harness session. When another assignee wrote since the last check, the agent gets this in its context:

```text
a-stack: New Task mail on "Build the parser"; run mail check.
```

The note names only the Task titles: no sender and no mail text. The agent then reads the mail as usual, with its own `mail check --cursor-file`.

The hook keeps its own mail cursor file, so it never moves the Agent's cursor file. There is one per Agent and harness session: `~/.cache/a-stack/hooks/AGENT_ID/HASH.json` (below `$XDG_CACHE_HOME` when set; on macOS below `~/Library/Caches`; on Windows `%LOCALAPPDATA%\a-stack-hooks\AGENT_ID\HASH.json`), where `HASH` comes from the harness session ID. The folder has mode 0700 and the files 0600. The hook removes files unused for 7 days. Deleting the folder is safe.

- At `SessionStart`, or at the first check of a harness session, the hook reads up to the newest message and says nothing about earlier mail. The reminder already says to run `mail check`. When this first check runs out of time on a long backlog, it keeps its place in `HASH.first.json`, and the next hook run goes on from there, still without a note.
- Each check moves the cursor past the messages it saw, so the hook names a message once. Mail the Agent sent itself is skipped.
- Of two hook runs of the same Agent at once, one checks and the other skips the check.
- When the check fails, the hook waits 30 seconds before it checks again. When the cursor file is broken or a cursor no longer fits, it starts again at the newest message.

The added cost per tool call, measured with a local service: under 1 ms when the last check was less than 30 seconds ago, and about 5 to 13 ms for a check. A note costs one more read for each Task with new mail.

Claude Code and Codex both take the note as `hookSpecificOutput.additionalContext`. Codex runs `PostToolUse` only for its local tools, not for hosted tools such as web search.
