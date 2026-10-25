# Work as the Orchestrator

The Orchestrator coordinates the Session's team. It registers Agents, creates Tasks, assigns Agents and adds dependencies. Only the Operator appoints the Orchestrator, closes the Session and answers Needs you questions.

The Operator follows the Session through its Task graph, so plan the work as a graph. Every piece of work is a Task, your own included, and every Task is linked to the Tasks it comes from and leads to. Unlinked Tasks do not show the Operator the order of the work.

Set up `me` as [SKILL.md](../SKILL.md) shows, with your own credential. If nobody gave you one, make it yourself with `a-stack new --orchestrator 01-orchestrator --no-browser --json` (see SKILL.md); do not ask the Operator for IDs. For each command's flags, see [commands.md](commands.md).

## 1. Register the Agents

Make one private credential folder for the Agents that you register. It must be outside every Git checkout, because a-stack rejects a credential file inside one:

```sh
umask 077; CREDS="$(mktemp -d)"
me agent register --name 02-worker --new-credential-file "$CREDS/02-worker.json" --operation-id orch-register-02-worker
```

- `data.resource_id` of the receipt is the new Agent's ID. `--agent` and `--assign` take this ID or the name.
- The new credential file names the Workspace, the Session and the Agent, so the Agent's `me` needs only its path.
- A name has 1 to 64 letters, digits, `_` or `-`, starts with a letter or digit, and is unique in the Workspace.
- Your own Agent credential is enough. Never use the Operator credential, and never run the Operator commands `a-stack` with no command, `a-stack new` or `a-stack -r`: they use the Operator credential and the current folder, and can create Workspaces, Sessions or a second service.
- If the response is lost, run the same command again with the same operation ID and file name. Never delete a credential file because a response was lost.
- To link the Agent's own worktree, add `--worktree ABSOLUTE_DIR`.

## 2. Create, link and assign each Task in one command

```sh
me task create --title 'Build the API' --body-file /your/private/folder/api.md \
  --after DESIGN_TASK_ID --assign 02-worker --assign 01-orchestrator --operation-id orch-task-api-1
```

- `--after T` makes the new Task depend on Task `T` (the arrow goes from T to the new Task). Repeat it for each prerequisite. `--assign` takes an Agent name or ID; repeat it for each assignee. All of it happens in one operation: if one prerequisite or Agent is wrong, nothing is created, and a retry with the same operation ID is safe.
- Create the Tasks in order, starting points first, so each Task's prerequisites exist when you create it. Then no Task needs a link later, and you never need a `graph_revision`.
- `data.resource_id` is the Task ID. A new Task is Waiting. The receipt's `revision` is the Task's revision, 1 plus the number of assignees: give it to your next change of that Task. Read the Task again only after a `conflict`.
- Assign yourself to every Task you hand out (`--assign` with your own name). Only assignees can read and send a Task's mail, so without it the other Agents cannot reach you there, and you cannot reopen the Task.
- Write the description so that the assignee can do the Task from it alone: the goal, the files, what done means.
- `--ticket '#42'` links a Ticket. Each Ticket reference is unique in the Session.
- Make Tasks for your own work too, such as merging, reviewing or releasing, and assign yourself.
- Link what the work really depends on. For example: an audit or a research Task to each fix or build that it leads to, a build to its review, and every Task that goes into a release to the release Task.
- Only a starting point, such as the first audit, has no prerequisite. When you create a Task without `--after` while other Tasks are open, `task create` prints a hint on stderr: check that the Task really is a starting point.
- A dependency does not block anything. The skill has each assignee run `task dependencies` before it starts and wait while a prerequisite is open.

## 3. Link or assign later

When you find a missing link or assignee after the create:

```sh
me dependency add --from A --to B --expected-revision G --operation-id orch-dep-a-b-1
me task assign --task T --agent 03-worker --expected-revision R --operation-id orch-assign-t-03
```

- `--from A --to B` means that B depends on A. a-stack rejects a duplicate, a dependency of a Task on itself and a cycle.
- `G` is the Session's `graph_revision`, not a Task revision. Read it with `me session inspect` right before, because each `task create` increases it. The receipt of a dependency change has the new `graph_revision` in `revision`: give it to your next dependency change.
- `R` is the Task's revision from its last receipt.
- Before you brief anyone, run `me task list` and look at `prerequisite_count`: a Task with 0 that is not a starting point is missing a link.

## 4. Brief each Agent

Give each Agent only what it needs, in private:

- That it is an a-stack Agent, and that it should load the `a-stack` skill.
- The path of its own credential file. Never paste the file's contents.
- The Workspace ID and the Session ID.
- Its Agent name and its Task IDs.
- The state directory, only if it is not the default.
- Its own private folder for helpers, for example `/tmp/work/02-worker/`. Give each Agent a different folder; the Agent creates it if it does not exist.
- The names of the other assignees of its Tasks, for `@name` mentions.
- The repository or worktree to work in, when the Task changes files.
- The hook install command, which the Agent runs once in that folder with the flag of its harness: `a-stack hook install --credential-file /private/creds/02-worker.json --claude` (or `--codex`, `--pi`, `--opencode`), with `--state-dir` when it is not the default. The hooks remind the Agent who it is after a compaction, keep it from stopping with a working Task that has no report, and tell it about new Task mail.

A harness reads its hooks when a session starts, so an Agent gets them from its next start or resume; when you start the Agent yourself, run its install first. A Claude Code subagent gets no hooks of its own: in the parent's folder its events are the parent's, and in a worktree that Claude Code makes for it, the folder is new. Brief it all the same; it works without hooks.

Keep the Operator credential, other Agents' credentials and the browser URL out of every brief.

Every Agent reads the Workspace's standing orders through a-stack, so do not copy them into briefs. When the Operator gives you a rule for all Agents, such as how to sign commits, suggest that they set it as a standing order with `a-stack workspace orders set` (an Operator command; see `a-stack help workspace orders set`).

Give each brief to its Agent yourself when you can, for example when you start the Agent. When the Agent is a coding agent that the Operator runs, such as an open Claude Code or Codex window, give the Operator one short brief per Agent to paste there. Do the rest yourself: register the Agents, create and link the Tasks, and write the briefs.

A brief can be this short:

```text
You are an a-stack Agent. Load the a-stack skill and follow its work loop.
Credential file: /private/creds/02-worker.json
Workspace: WORKSPACE_ID   Session: SESSION_ID
State directory: /private/state   (only if not the default)
Your name: 02-worker   Your private folder: /tmp/work/02-worker/
Work in: /path/to/worktree
Hooks: run once in that folder: a-stack hook install --credential-file /private/creds/02-worker.json --claude (or --codex, --pi, --opencode)
Tasks: A_ID (do first), B_ID (depends on A)
Other assignees you can @mention: 01-orchestrator
```

## 5. Watch the work

- `me task list` shows each Task's `progress`, `needs_you` and `unassigned`.
- `me session snapshot --known-revision N` returns `unchanged: true` while nothing changed since read revision N. Then you need not read the pages again.
- `me task show --task T` shows the report, and `me task history --task T` shows every change.
- `me mail check --cursor-file /your/private/folder/mail.json` reads the new mail of the Tasks that you are assigned to.

When a Task shows `needs_you`, the Operator must answer. You cannot answer for the Operator. The Operator answers with `a-stack answer` or in the browser view, and the Task's assignees get the answer as Task mail from the Operator.

## 6. Change the plan

- When new work turns up, create a Task for it with `--after` and `--assign` in the same command, also when you do the work yourself, so that the Operator sees it.
- Change the title, description or Ticket with `task edit`.
- Move work with `task unassign` and `task assign`.
- When an Agent stopped, for example at a usage limit or after a crash (`quiet` in `task list` hints at it), hand its Task to a fresh Agent: assign the new Agent, make sure you are assigned too, and put the output of `me task handoff --task T` in the new Agent's brief. It has the description, the report, the open question, the unfinished prerequisites, the assignees and the latest Task mail.
- To reopen a Finished or Cancelled Task, assign yourself, then run `task reopen --reason`. Only an assignee can reopen.
- Archive finished side work that nobody needs to see any more with `task archive`, so the Operator's graph stays on the live work. You do not need to be assigned. a-stack already hides a done Task when every Task that depends on it is done, or a day after it ended. `task unarchive` undoes your archive, and `task reopen` clears it.
- Remove a wrong dependency with `dependency remove`.

## 7. Keep the record in Task mail

Write decisions, hand-offs and review results in the Task's mail, so that every assignee and a later Orchestrator can read them. A conversation with one Agent is not part of the record.
