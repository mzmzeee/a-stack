# Agent hooks

Claude Code, Codex, Pi and OpenCode can run a command on events in an agent's session; Pi does it through an a-stack extension and OpenCode through an a-stack plugin. `a-stack hook install` adds such hooks for one Agent. They do four things:

- When a session starts, resumes, is cleared or is compacted, the agent gets a short reminder in its context: its a-stack name and Session, how to run a-stack as itself, its open Tasks with their status, to load the `a-stack` skill before running commands and then run `mail check`, and the Workspace's [standing orders](lifecycle.md#standing-orders) (the first 8 lines, and the command for the rest). So the agent keeps the a-stack protocol after a compaction.
- The Agent's runtime state in the Operator's view follows the agent: `working` when you send a prompt and after each tool call, `blocked` when it waits for a permission or an input dialog, and `idle` when it stops.
- The [Stop guard](#stop-guard) keeps the agent from stopping once while a Task that it set `working` has no report.
- The [Task mail nudge](#task-mail-nudge) tells the agent when another assignee wrote Task mail, at most every 30 seconds.

## Install

Run the command in the Agent's working folder, with the values from its brief. The brief of `a-stack new --orchestrator` names it for the Orchestrator, and the Orchestrator's briefs name it for each Agent ([orchestrator reference](../skills/a-stack/references/orchestrator.md#4-brief-each-agent)):

```sh
a-stack hook install --credential-file /path/to/02-worker.json \
  --workspace WORKSPACE_ID --session SESSION_ID --name 02-worker \
  --runtime-id RUNTIME_ID
```

- A credential file written by v0.1.5 or later names its Workspace, Session and Agent, so `--workspace`, `--session` and `--name` can be left out: `a-stack hook install --credential-file FILE --runtime-id RUNTIME_ID` is enough. A `--workspace` or `--session` that you give must match the file. For an older file, `hook install` asks the service for the Workspace, Session and Agent of its credential. This works without `--state-dir` too: discovery uses the default state directory, not the working folder.
- `--name` finds the Agent's ID with the service. Instead, `--agent ID` gives the ID and needs no running service.
- `--runtime-id` is the instance ID of the Agent's runtime binding, as in `agent attach --runtime-id`. a-stack records runtime state only for an attached Agent with the same runtime ID. Without `--runtime-id`, the hooks record no runtime state. The reminder, the Stop guard and the Task mail nudge still work. To attach a new Agent first, see [Runtime bindings](lifecycle.md#runtime-bindings).
- `--state-dir` goes into the hook command when you give it.
- `--worktree DIR` is the folder the hooks act in; it defaults to the current folder. The hooks do nothing in a session whose folder is outside it.
- `--no-stop-guard` and `--no-mail-nudge` turn off the Stop guard and the Task mail nudge. Both are on by default. Run install again without them to turn them on.

The hook command names the a-stack binary, the credential file path, the Workspace, Session and Agent IDs, the runtime ID and the folder, all explicitly. It does not use environment variables or the current folder to choose an identity. The credential file stays where it is; the settings hold only its path.

By default the command writes the user settings of Claude Code and Codex. With `--pi` it writes Pi's files, with `--opencode` OpenCode's:

| Harness | User settings | With `--project` |
| --- | --- | --- |
| Claude Code | `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR/settings.json`) | `.claude/settings.local.json` |
| Codex | `~/.codex/hooks.json` (or `$CODEX_HOME/hooks.json`) | `.codex/hooks.json` |
| Pi | `~/.pi/agent/a-stack-hooks.json` and `~/.pi/agent/extensions/a-stack.ts` (or in `$PI_CODING_AGENT_DIR`) | none |
| OpenCode | `~/.config/opencode/a-stack-hooks.json` and `~/.config/opencode/plugins/a-stack.js` (or in `$XDG_CONFIG_HOME/opencode`, or in `$OPENCODE_CONFIG_DIR`) | none |

`--claude`, `--codex`, `--pi` or `--opencode` writes only those you name; `--pi` or `--opencode` alone leaves Claude Code and Codex out. With `--project` the files are in the current folder. For Claude Code that is `settings.local.json`, which is not meant to be committed, because the hooks name a private credential path. Several Agents can have hooks in the same user settings, one set per folder.

The command keeps every other setting and hook in the file. It replaces only the a-stack hooks of the same folder, so you can run it again after a change. It rewrites the file with sorted keys and two-space indents. When the file is a link, it writes the file the link points to. It refuses a file that is not a JSON object.

Codex runs a new or changed hook only after you trust it: run `/hooks` in Codex and trust the a-stack hooks. Project hooks in `.codex/` also need a trusted project.

To remove the hooks of the current folder, run `a-stack hook uninstall` (or with `--worktree DIR`), with the same `--project`, `--claude`, `--codex`, `--pi` or `--opencode` as for install. It removes only a-stack's hooks for that folder.

### Pi

Pi has no command hooks, so `hook install --pi` writes two files in Pi's agent folder, `~/.pi/agent` or `$PI_CODING_AGENT_DIR` (an absolute path):

- `a-stack-hooks.json` holds the hook commands, in the same form as Claude Code's settings. Several folders can have hooks in it, one set each.
- `extensions/a-stack.ts` is a small extension that comes with the a-stack binary. On Pi's events it runs those commands with the JSON that Claude Code would send, and passes their output back to Pi. It needs Pi 0.87 or later and `/bin/sh`, so it works on Linux and macOS; on Windows, `hook install --pi` refuses with `invalid_input`.

Pi loads the extension when it starts; in a running Pi, run `/reload`. The reminder goes into the agent's context as a hidden message, and the mail nudge is added to the tool result or to the prompt. When the Stop guard blocks, Pi makes one more model request with the reason as a message. `a-stack update` refreshes the extension automatically if it still matches a known released version. It leaves edited, foreign and linked files alone, and never rewrites the hook list. Use `--no-refresh` to skip this, or `a-stack update --refresh` to refresh without updating the binary; then run `/reload` in Pi. `hook uninstall --pi` removes both files once no folder has hooks left. Another file named `a-stack.ts` in the extensions folder is never changed.

Pi hooks have no `--project`: Pi loads a project's extensions only after you trust the project, and the hook list would put a credential path in it. `a-stack skill install` already gives Pi the skill, in `~/.agents/skills`.

### OpenCode

OpenCode has no command hooks either, so `hook install --opencode` writes two files in OpenCode's global config folder: `$OPENCODE_CONFIG_DIR` when it is set, else `~/.config/opencode` (`$XDG_CONFIG_HOME/opencode` when that is set, also on macOS). The folders must be absolute paths.

- `a-stack-hooks.json` holds the hook commands, in the same form as for Pi.
- `plugins/a-stack.js` is a small plugin that comes with the a-stack binary. It runs those commands with the JSON that Claude Code would send, and passes their output back to OpenCode. It needs OpenCode 1.19 or later and `/bin/sh`, so it works on Linux and macOS; on Windows, `hook install --opencode` refuses with `invalid_input`.

OpenCode loads the plugin when it starts, in its TUI and in `opencode serve` or `opencode run`; restart a running OpenCode after install. The reminder goes into the session's system prompt, and again after a compaction. The mail nudge is added to the tool output, or to the prompt as hidden text. OpenCode cannot block a stop, so when the session goes idle and the Stop guard blocks, the plugin starts one new turn with the reason as the prompt, with the same agent and model. It does not do that after an error or after you abort the turn. A subagent's session gets no reminder, nudge or Stop guard; its permission questions still record `blocked`. When the plugin cannot ask OpenCode whether a session is a subagent's, it skips the session the same way, until OpenCode reports a change to the session. `opencode run` ends at the first idle, so there the Stop guard's turn may not run. `a-stack update` refreshes the plugin automatically if it still matches a known released version. It leaves edited, foreign and linked files alone, and never rewrites the hook list. Use `--no-refresh` to skip this, or `a-stack update --refresh` to refresh without updating the binary; then restart OpenCode. `hook uninstall --opencode` removes both files once no folder has hooks left. Another file named `a-stack.js` in the plugins folder is never changed.

OpenCode hooks have no `--project`: the hook list would put a credential path in the project's `.opencode` folder. `a-stack skill install` already gives OpenCode the skill, in `~/.agents/skills`.

Cursor CLI and GitHub Copilot CLI also read Claude Code's settings files: Cursor the user's `~/.claude/settings.json`, Copilot a project's `.claude/settings.json` and `.claude/settings.local.json`. a-stack does not support them yet, so the hooks there may do nothing or misfire; Copilot, for example, would record `blocked` before each tool call.

## Events

| Event | Pi event | OpenCode | What the hook does |
| --- | --- | --- | --- |
| `SessionStart` | `session_start`, `session_compact` | first request of a session, `session.compacted` | Prints the reminder as `hookSpecificOutput.additionalContext` JSON. The harnesses add it to the agent's context. Starts the Task mail nudge's cursor file. |
| `UserPromptSubmit`, `PostToolUse` | `before_agent_start`, `tool_result` | `chat.message`, `tool.execute.after` | Records `working`. Runs the Task mail nudge. |
| `PermissionRequest` | `ui_prompt_start` | `permission.asked`, `question.asked` | Records `blocked`. The hook makes no permission decision. |
| `Notification` with `elicitation_dialog` (Claude Code only) | | | Records `blocked`. |
| `Stop` | `agent_before_settle` | `session.idle` | Runs the Stop guard. When the guard does not block, records `idle`. |
| `SubagentStart` (Claude Code only) | | | Prints the reminder for a [subagent](#claude-code-subagents) of the Agent's own. |
| `SubagentStop` (Claude Code only) | | | Runs the Stop guard for such a subagent. When the guard does not block, records `idle`. |

A state hook reads the Agent first and writes only when the state changes, so a tool call usually costs one read. The harness gives each hook 5 seconds, and the hook stops itself after 2. On any error, also when the service is down, the hook prints nothing and exits with 0, so it never stops or slows the agent. `a-stack hook` itself reads the event as JSON on stdin; the harness runs it, not you.

### Claude Code subagents

A Claude Code subagent's hook input names the parent's session, its own folder as `cwd`, and its `agent_id`. Measured with Claude Code 2.1.310: a subagent started with `isolation: "worktree"` reports its new worktree, `.claude/worktrees/agent-ID` below the parent's folder, and a subagent without it reports the parent's folder, also after a `cd` in its shell. So a subagent's events, `SubagentStart`, its tool calls and `SubagentStop`, reach the hooks of every folder that holds its own.

An event with `agent_id` counts only for the hooks of exactly the subagent's folder, and only when the parent session is not that folder's Agent, a main session that ran in the folder. The hooks of a folder above it, such as the Orchestrator's, and the hooks of the parent's own folder skip it: they record no state, show no mail note and do not guard the subagent's stop. For a subagent that counts, the cursor file and the Stop guard marker are its own. The hook remembers that a main session ran in its folder with an empty marker file, `HASH.main`, next to the cursor files.

A main session's folder follows a `cd` in its shell. Measured with Claude Code 2.1.310: a session started in the project folder that runs `cd .claude/worktrees/w1` reports that worktree in its next events, until it leaves it. So the Orchestrator's session can reach a worker's folder, such as to look at or merge its work. A main session that started in a folder that holds the hook's folder is never that folder's Agent. Its `SessionStart` leaves an empty marker file, `HASH.away`, and the hooks of the folder below then skip its events there: they record no state, show no mail note and do not guard its stop, and the worker's subagents of that session keep their hooks. The `SessionStart` after a compaction is not a start. A session that started outside the folder in another way, such as with `--add-dir`, counts in it as before.

Claude Code reads hooks when a session starts, for the session and its subagents: hooks added during a session ran only after `claude --continue`. A worktree that Claude Code makes for a subagent is new, so no hooks were installed for it at that time. In practice a Claude Code subagent gets no a-stack hooks of its own, and the rule above keeps the parent's hooks from acting for it. `SubagentStart` and `SubagentStop` count for a subagent whose worktree had hooks when the parent session started or resumed.

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

The hook prints `{"decision":"block","reason":"..."}`, the same for Claude Code and Codex. Claude Code shows the reason as Stop hook feedback; Codex continues the turn with the reason as a new prompt; the Pi extension adds it as a message for one more model request; the OpenCode plugin sends it as a new prompt.

The guard costs two reads at each stop: the assignments and the Task list. A working Task whose latest change is not its status change also costs a read of its history, to find who set it working. A block costs one more read, to check that the Session is open.

## Task mail nudge

After a tool call or a prompt, the hook checks the Agent's Task mail, at most once every 30 seconds per harness session. When another assignee wrote since the last check, the agent gets this in its context:

```text
a-stack: New Task mail on "Build the parser"; load the a-stack skill, then run mail check.
```

The note names only the Task titles: no sender and no mail text. The agent then reads the mail as usual, with its own `mail check --cursor-file`.

The hook keeps its own mail cursor file, so it never moves the Agent's cursor file. There is one per Agent and harness session: `~/.cache/a-stack/hooks/AGENT_ID/HASH.json` (below `$XDG_CACHE_HOME` when set; on macOS below `~/Library/Caches`; on Windows `%LOCALAPPDATA%\a-stack-hooks\AGENT_ID\HASH.json`), where `HASH` comes from the harness session ID. The folder has mode 0700 and the files 0600. The hook removes files unused for 7 days. Deleting the folder is safe.

- At `SessionStart`, or at the first check of a harness session, the hook reads up to the newest message and says nothing about earlier mail. The reminder already says to run `mail check`. When this first check runs out of time on a long backlog, it keeps its place in `HASH.first.json`, and the next hook run goes on from there, still without a note.
- Each check moves the cursor past the messages it saw, so the hook names a message once. Mail the Agent sent itself is skipped.
- Of two hook runs of the same Agent at once, one checks and the other skips the check.
- When the check fails, the hook waits 30 seconds before it checks again. When the cursor file is broken or a cursor no longer fits, it starts again at the newest message.

The added cost per tool call, measured with a local service: under 1 ms when the last check was less than 30 seconds ago, and about 5 to 13 ms for a check. A note costs one more read for each Task with new mail.

Claude Code, Codex, the Pi extension and the OpenCode plugin take the note as `hookSpecificOutput.additionalContext`. Codex runs `PostToolUse` only for its local tools, not for hosted tools such as web search.
