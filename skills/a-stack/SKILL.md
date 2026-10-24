---
name: a-stack
description: Work as an Agent in an a-stack Session with the a-stack CLI (Tasks, Task mail, revisions, operation IDs, Needs you). Use when you are told you are an a-stack Agent or Orchestrator, are given an a-stack credential file, or must coordinate Tasks or Task mail with other agents.
---

# Work in an a-stack Session

a-stack is a local service and CLI that lets a human Operator watch a team of AI coding Agents. The Agents share a Task graph, one Task mail thread per Task and Needs you questions through the `a-stack` CLI, and the Operator follows them in a read-only browser view.

If you are the Session's Orchestrator, also read [references/orchestrator.md](references/orchestrator.md). For every command, flag and error, read [references/commands.md](references/commands.md).

## What you receive

The Operator or the Orchestrator gives you:

- The `a-stack` binary on your `PATH`.
- The path of your credential file.
- The Workspace ID and the Session ID.
- Your Agent name, such as `02-worker`, and your Task IDs.
- A state directory, only if the service does not use the default one.

If something is missing, ask the one who briefed you. Never guess an ID.

Your Workspace and Session already exist. To set up means only to write the helpers below. Never run `a-stack` with no command, `a-stack new` or `a-stack -r` (see Rules).

## Set up your helpers

Use the private folder you were given (create it with `mkdir -p -m 700` if it does not exist), or make one with `mktemp -d`. Keep every helper, cursor file and draft there. It is not the state directory, and it is never shared: Agents that share a folder overwrite each other's helpers.

Write two small scripts in your private folder, with your real values, and make them executable with `chmod +x`. Leave out the `--state-dir` line if you were given no state directory.

`me` runs a-stack as you:

```sh
#!/bin/sh
exec a-stack --state-dir /path/to/state \
  --as agent --credential-file /path/to/02-worker.json \
  --workspace WORKSPACE_ID --session SESSION_ID "$@"
```

`rev` prints a Task's current revision:

```sh
#!/bin/sh
"$(dirname "$0")/me" task show --task "$1" | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["revision"])'
```

Scripts keep working in shells that forget functions between commands. Write them with your file-write tool if you have one, and call them by their literal full path, not through a variable. Below, `me` and `rev` stand for these full paths. Check the setup with `me session inspect`. If your harness rejects `$(rev T)` inside a command, run `rev T` first and use the number it prints.

## Work loop

Do this for each of your Tasks. `T` is the Task ID, and `02-worker` stands for your name.

1. Read the Task with `me task show --task T` and the Task mail with `me mail check`.
2. Start (a new Task is Waiting until an assignee starts it): `me task status --task T --progress working --expected-revision "$(rev T)" --operation-id 02-worker-start-1`.
3. Do the work. At each milestone, send one short Task mail: `me mail send --task T --body 'Tests pass; writing docs next' --operation-id 02-worker-mail-01`.
4. Run `me mail check` between big steps and before you finish. Answer what other assignees ask you.
5. Write the report: `me task report --task T --report 'What was done, and where' --expected-revision "$(rev T)" --operation-id 02-worker-report-1`. For long text, use `--body-file FILE` or `--body-file -` (stdin) instead of `--report`. The report is shared and each report replaces the last one, so read it first when the Task has other assignees.
6. Tell the other assignees in Task mail what you did. Then finish when the whole shared Task is done: `me task status --task T --progress finished --summary 'What was done' --expected-revision "$(rev T)" --operation-id 02-worker-finish-1`. Finishing needs no approval, and the Orchestrator does not finish your Task for you. If only your part is done, say so in Task mail and leave the Task Working.

When you wait for another Task, set `--progress waiting --reason 'Waits for the API Task'`.

When only the Operator can decide, ask one clear question with `me task attention set --task T --question 'Which release gets this?' --expected-revision "$(rev T)" --operation-id 02-worker-ask-1`. Then stop, and say in your conversation that you wait for the Operator's answer. After the answer, run `task attention resolve` with a `--note` (see [references/commands.md](references/commands.md)).

## Rules

- Every change needs `--operation-id` with 8 to 128 letters, digits, `_` or `-`. A shorter ID fails with a generic `invalid_input` message. Use a new, unique ID for each change, such as `02-worker-mail-03`. Reuse an ID only to retry the same unchanged command after a lost response.
- Every Task change needs `--expected-revision` with the Task's current `revision`. On `conflict`, read the Task again, decide whether the change still applies, and send it with the new revision and a NEW operation ID.
- Each `@name` in a mail body must be a current assignee of that Task, or the whole send fails with `mention target must be a current Task assignee`. Check the names with `me task assignees --task T`, and write other names without `@`. Only assignees can read a Task's mail, so the Orchestrator reads it only when it is assigned.
- Refer to a credential file only by its path. Never print, paste, log or commit its contents, or the part of a browser URL after `#`.
- Run every command as yourself, through `me`: with `--as agent`, your own `--credential-file`, `--workspace` and `--session`. No Agent command uses the current folder to choose a Workspace or a Session.
- Use only the commands of the work loop and the references. `a-stack` with no command, `a-stack new` and `a-stack -r` (`--resume`) are Operator commands: they use the Operator credential and the current folder, can create Workspaces and Sessions, and can start a second service in the foreground. Never run them, also when you are told to "set up your Session". Your Session already exists; check it with `me session inspect`.
- Success prints a `data` JSON object on stdout. Failure prints an `error` JSON object (`code`, `message`, `retryable`) on stderr and exits with 2 for `invalid_input` and 1 for all other errors. Check the exit code of each change.
- The Operator runs the service; leave it running. Never stop it, and never run `a-stack serve` on its state directory. To test a-stack itself, start a disposable service with a `mktemp -d` state directory and give `--state-dir` to every command. Changing `HOME` is not enough, because the Operator's `XDG_STATE_HOME` can still lead to the real state.
