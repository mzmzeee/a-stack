# Work as the Orchestrator

The Orchestrator coordinates the Session's team. It registers Agents, creates Tasks, assigns Agents and adds dependencies. Only the Operator appoints the Orchestrator, closes the Session and answers Needs you questions.

Set up `me` and `rev` as [SKILL.md](../SKILL.md) shows, with your own credential. Add a third script, `grev`, that prints the Session's graph revision:

```sh
#!/bin/sh
"$(dirname "$0")/me" session inspect | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["graph_revision"])'
```

For each command's flags, see [commands.md](commands.md).

## 1. Register the Agents

Make one private credential folder for the Agents that you register. It must be outside every Git checkout, because a-stack rejects a credential file inside one:

```sh
umask 077; CREDS="$(mktemp -d)"
me agent register --name 02-worker --new-credential-file "$CREDS/02-worker.json" --operation-id orch-register-02-worker
```

- `data.resource_id` of the receipt is the new Agent's ID. Task commands take this ID, not the name.
- A name has 1 to 64 letters, digits, `_` or `-`, starts with a letter or digit, and is unique in the Workspace.
- Your own Agent credential is enough. Never use the Operator credential, and never run the Operator commands `a-stack` with no command, `a-stack new` or `a-stack -r`: they use the Operator credential and the current folder, and can create Workspaces, Sessions or a second service.
- If the response is lost, run the same command again with the same operation ID and file name. Never delete a credential file because a response was lost.
- To link the Agent's own worktree, add `--worktree ABSOLUTE_DIR`.

## 2. Create and assign the Tasks

```sh
me task create --title 'Build the API' --body-file /your/private/folder/api.md --operation-id orch-task-api-1
me task assign --task T --agent AGENT_ID --expected-revision "$(rev T)" --operation-id orch-assign-api-02
```

- `data.resource_id` of `task create` is the Task ID. A new Task is Waiting, with revision 1.
- Write the description so that the assignee can do the Task from it alone: the goal, the files, what done means.
- `--ticket '#42'` links a Ticket. Each Ticket reference is unique in the Session.
- Assign yourself too (your own Agent ID is `orchestrator_id` in `me session inspect`) when you want to read or send the Task's mail, or to reopen the Task, for example on review Tasks. Without it, you see only the progress and the report, and nobody can `@mention` you there.

## 3. Add dependencies

```sh
me dependency add --from A --to B --expected-revision "$(grev)" --operation-id orch-dep-a-b-1
```

- `--from A --to B` means that B depends on A.
- The expected revision is the Session's `graph_revision`, not a Task revision. Each `task create` and each dependency change increases it, so read it again before each change. The receipt's `revision` is the new `graph_revision`.
- a-stack rejects a duplicate, a dependency of a Task on itself and a cycle.
- A dependency does not block anything. Say in B's description that B waits for A, so that its assignee sets Waiting until A is Finished.

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

Keep the Operator credential, other Agents' credentials and the browser URL out of every brief.

A brief can be this short:

```text
You are an a-stack Agent. Load the a-stack skill and follow its work loop.
Credential file: /private/creds/02-worker.json
Workspace: WORKSPACE_ID   Session: SESSION_ID
State directory: /private/state   (only if not the default)
Your name: 02-worker   Your private folder: /tmp/work/02-worker/
Work in: /path/to/worktree
Tasks: A_ID (do first), B_ID (depends on A; set it Waiting until A is Finished)
Other assignees you can @mention: 01-orchestrator
```

## 5. Watch the work

- `me task list` shows each Task's `progress`, `needs_you` and `unassigned`.
- `me session snapshot --known-revision N` returns `unchanged: true` while nothing changed since read revision N. Then you need not read the pages again.
- `me task show --task T` shows the report, and `me task history --task T` shows every change.
- `me mail check` reads the mail of the Tasks that you are assigned to.

When a Task shows `needs_you`, the Operator must answer. You cannot answer for the Operator.

## 6. Change the plan

- Change the title, description or Ticket with `task edit`.
- Move work with `task unassign` and `task assign`.
- To reopen a Finished or Cancelled Task, assign yourself, then run `task reopen --reason`. Only an assignee can reopen.
- Remove a wrong dependency with `dependency remove`.

## 7. Keep the record in Task mail

Write decisions, hand-offs and review results in the Task's mail, so that every assignee and a later Orchestrator can read them. A conversation with one Agent is not part of the record.
