---
name: a-stack
description: 'REQUIRED before the first a-stack command or me helper that wraps it, and again after a context reset. Load this skill when briefed as an a-stack Agent (such as 02-worker), given an a-stack credential file, Task IDs or Workspace/Session IDs, or asked to coordinate agents through a-stack. Also load for a-stack notices or reminders such as "New a-stack Task mail mentions you" or "a-stack: New Task mail on ..."; instructions to use a-stack Task mail, Needs you, a report, task handoff or standing orders; or a request to be the a-stack Orchestrator or set a-stack up. Covers the CLI work loop, revisions, operation IDs and credentials for Claude Code, Codex, Pi and OpenCode.'
---

# Work in an a-stack Session

## Load this skill when

Read this file before the first `a-stack` command (including a `me` helper), and again after a context reset or compaction.

- You receive an a-stack Agent role, credential file, Task IDs or Workspace/Session IDs.
- You see an a-stack notice or reminder to check Task mail.
- You are told to use a-stack Task mail, Needs you, reports, `task handoff` or standing orders, or to coordinate agents through a-stack.
- You are asked to be the a-stack Orchestrator or set a-stack up.

a-stack is a local service and CLI that lets a human Operator watch a team of AI coding Agents. The Agents share a Task graph, one Task mail thread per Task and Needs you questions through the `a-stack` CLI, and the Operator follows them in a browser view, where the only thing the Operator can do is answer a Needs you question.

If you are the Session's Orchestrator, also read [references/orchestrator.md](references/orchestrator.md). In short: create each Task with its prerequisites and assignees in one command, `task create --title ... --after PREREQUISITE_TASK_ID --assign 02-worker --assign 01-orchestrator`, so the Operator's graph shows the order of the work. For every command, flag and error, read [references/commands.md](references/commands.md).

## Set yourself up when nobody briefed you

If the Operator asks you directly to use a-stack or to be the Orchestrator, gives you no credential file, and nothing earlier in this conversation briefed you as an Agent, set yourself up. Do not ask the Operator for IDs or paths. If you were briefed as an Agent before and lost the details, ask the one who briefed you instead; do not set yourself up as the Orchestrator.

1. Run `a-stack new --orchestrator 01-orchestrator --no-browser --json` in the project folder. It makes or reuses the folder's Workspace and Session, registers you, appoints you as the Orchestrator and prints `orchestrator_credential_file`, `workspace_id`, `session_id` and a `brief`. Running it again returns the same hand-off, so it is safe after a lost answer or in a new conversation. If the output has `"existing_orchestrator": true`, the Session already had this Orchestrator: tell the Operator that you are taking over as `01-orchestrator`, and stop if they say another conversation still is.
2. If it says the folder is inside another Workspace, run step 1 in that folder. If it fails because the service is not running, run `a-stack service install` (a user service, no sudo), tell the Operator that you did, then run step 1 again.
3. If it says the Session already has an Orchestrator, ask the Operator whether to use another Session or to replace the Orchestrator. For another Session in the same folder, use a new Session name and a new Agent name, because Agent names are unique in a project folder: `a-stack new NAME --orchestrator 01-orchestrator-NAME --no-browser --json`.
4. Follow the brief: set up `me` below with the credential file, install the hooks (below), then read [references/orchestrator.md](references/orchestrator.md). The output has no view link; tell the Operator to open the view with `a-stack`.

## What you receive

When an Orchestrator or the Operator briefs you, you get:

- The `a-stack` binary on your `PATH`.
- The path of your credential file.
- The Workspace ID and the Session ID. The CLI finds them from your credential file alone, so you need them only for an old service (see below).
- Your Agent name, such as `02-worker`, and your Task IDs.
- A state directory, only if the service does not use the default one.
- A private folder for your helper scripts. If you get none, make one with `mktemp -d`.

If something is missing, ask the one who briefed you, or find it yourself: `me session inspect` shows your Workspace, Session and Orchestrator, and `me task list` your Tasks. Never guess an ID.

Your Workspace and Session already exist. To set up means only to write the helper below. Apart from the self-setup above, never run `a-stack` with no command, `a-stack new` or `a-stack -r` (see Rules).

## Set up your helper

Use the private folder you were given (create it with `mkdir -p -m 700` if it does not exist), or make one with `mktemp -d`. Keep your helper, your mail cursor file and your drafts there. It is not the state directory, and it is never shared: Agents that share a folder overwrite each other's files.

Write a script named `me` in your private folder, with your real paths, and make it executable with `chmod +x`. It runs a-stack as you. Leave out `--state-dir /path/to/state` if you were given no state directory.

```sh
#!/bin/sh
exec a-stack --state-dir /path/to/state --as agent --credential-file /path/to/02-worker.json "$@"
```

Check it with `me session inspect`. If that fails with `this credential file is from before a-stack v0.1.5`, the service is too old to look up your Workspace and Session: add `--workspace WORKSPACE_ID --session SESSION_ID` from your brief before `"$@"`.

A script keeps working in shells that forget functions between commands. Write it with your file-write tool if you have one, and call it by its literal full path, not through a variable. Below, `me` stands for that full path, and `/your/private/folder/mail.json` for a file in your private folder.

## Install the hooks

Once, in the folder you work in, install the a-stack hooks for your harness: `a-stack hook install --credential-file /path/to/02-worker.json --claude` (`--codex`, `--pi` or `--opencode` for those; add `--state-dir` if you were given one). They remind you who you are after a compaction, keep you from stopping with a working Task that has no report, and tell you about new Task mail. They work from your next session start or resume; tell the Operator so. Skip this as a Claude Code subagent: the hooks would not reach you.

## If you run inside Herdr

If `HERDR_PANE_ID` is set in your shell, bind yourself to your pane once, so that a mention can reach you as a short notice: `me herdr bind --agent YOUR_AGENT_ID --expected-revision 1 --operation-id 02-worker-bind-1`. `me session inspect` shows your Agent ID as `your_agent_id`. If it fails with `integration_missing`, tell the Operator the `herdr integration install` command from the message and restart your harness in that pane after installation; do not install it yourself. An existing live binding needs the Operator's `agent rebind --clear-runtime` and a new credential before you bind again; a stale Herdr target can be replaced by your own `herdr bind`. Use your current revision from `me agent list`. Mail works the same without a binding.

## Standing orders

The Operator can give standing orders for the whole Workspace, such as how to sign commits or what never to change. They override your own defaults and your harness's memory, and they apply to every Task. Read them when you start and after a compaction: `me session inspect` shows them as `standing_orders.body` (left out when there are none), and `me workspace orders show` shows them alone. The a-stack hooks also put their first lines in your context. Only the Operator changes them. If your brief contradicts them, ask the one who briefed you.

## Work loop

Do this for each of your Tasks. `T` is the Task ID, `02-worker` stands for your name, and `R` is the Task's revision (see Rules).

1. Read the Task with `me task show --task T`. Its `revision` is your first `R`. Run `me task dependencies --task T`: while a prerequisite is not Finished, set Waiting with a reason that names it, and start only when it is done or its assignee says in Task mail that you can. Read your new Task mail with `me mail check --cursor-file /your/private/folder/mail.json`. If the output has `"more": true`, run the same check again. When you take over a Task that another Agent started, also read `me task handoff --task T`: the report, the open question and the latest Task mail. Check the mail coverage and omitted count; read earlier mail without a cursor or continue with the returned cursor when needed.
2. Start (a new Task is Waiting until an assignee starts it): `me task status --task T --progress working --expected-revision R --operation-id 02-worker-start-1`. The receipt's `revision` is your next `R`.
3. Before editing shared areas, claim the files or folders relative to the Workspace root: `me task claim --task T --path src --path docs/api.md --expected-revision R --operation-id 02-worker-claim-1`. Use the receipt's revision as your next `R`. Read its `warnings`: overlaps name the other Task and its Agents. Coordinate in Task mail before editing the same files; the warning is advisory, not a lock. `me task claims --task T` reads current claims and overlaps. Release paths with `task unclaim` using the same flags; finishing or cancelling releases all claims. Then do the work. At each milestone, send one short Task mail: `me mail send --task T --body 'Tests pass; writing docs next' --operation-id 02-worker-mail-01`. A Working Task with no change and no Task mail for 30 minutes shows the Operator that it is quiet, as if you had stopped.
4. Run `me mail check --cursor-file /your/private/folder/mail.json` again between big steps and before you finish. It shows only mail you have not seen. Answer what other assignees ask you with `me mail reply --message MESSAGE_ID --body 'Your answer' --operation-id 02-worker-reply-01`. It routes to the original Task and mentions its sender if still assigned; all current assignees can read it.
5. Write the report: `me task report --task T --report 'What was done, and where' --expected-revision R --operation-id 02-worker-report-1`. For long text, use `--body-file FILE` or `--body-file -` (stdin) instead of `--report`. The report is shared and each report replaces the last one, so read it first when the Task has other assignees.
6. Tell the other assignees in Task mail what you did. Then finish when the whole shared Task is done: `me task status --task T --progress finished --summary 'What was done' --expected-revision R --operation-id 02-worker-finish-1`. Finishing needs no approval, and the Orchestrator does not finish your Task for you. If only your part is done, say so in Task mail and leave the Task Working.

A finished Task stays in the Operator's graph until it is archived. The Orchestrator archives side work, and a-stack archives a done Task once every Task that depends on it is done, or a day after it ended.

When you wait for another Task, set `--progress waiting --reason 'Waits for the API Task'`.

If an a-stack hook blocks you from stopping, write the report and finish, or set Waiting with a reason. If it says there is new Task mail, run your `mail check`.

When only the Operator can decide, ask one clear question with `me task attention set --task T --question 'Which release gets this?' --expected-revision R --operation-id 02-worker-ask-1`. Then stop, and say in your conversation that you wait for the Operator's answer. The Operator answers in your conversation, or with `a-stack answer` or the browser view. That answer arrives as Task mail from the Operator (`sender_name` is `Operator`), and it already resolves the question: read it with `mail check`, and run `task show` again for the new revision. When the Operator answers in your conversation instead, run `task attention resolve` with a `--note` (see [references/commands.md](references/commands.md)).

## Rules

- Every change needs `--operation-id` with 8 to 128 letters, digits, `_` or `-`. Use a new, unique ID for each change, such as `02-worker-mail-03`. Reuse an ID only to retry the same unchanged command after a lost response.
- Every Task change needs `--expected-revision R`. `R` is the `revision` of the Task you read last, or of the receipt of your last change to that Task. A mail receipt's `revision` is the message number, not a Task revision. Do not read the Task again only to get `R`: the check exists to tell you that someone else changed the Task. On `conflict`, read the Task again, decide whether the change still applies, and send it with the new revision and a NEW operation ID.
- Each `@name` in a mail body must be a current assignee of that Task, or the whole send fails with `mention target must be a current Task assignee`. Check who can read and be mentioned with `me mail recipients --task T`, and write other names without `@`. Only assignees can read a Task's mail, so the Orchestrator reads it only when it is assigned.
- Refer to a credential file only by its path. Never print, paste, log or commit its contents, or the part of a browser URL after `#`.
- Run every command as yourself, through `me`: with `--as agent` and your own `--credential-file`, which names your Workspace and Session. No Agent command uses the current folder to choose a Workspace or a Session.
- Keep every piece of work on a Task, so the Operator sees it. When you find work outside your Tasks, ask the Orchestrator in Task mail to create and link a Task for it; do not do it quietly. Keep `progress` true: Working while you work, Waiting with a reason while you wait, and Finished with a report when done.
- Use only the commands of the work loop and the references. `a-stack` with no command, `a-stack new` and `a-stack -r` (`--resume`) are Operator commands: they use the Operator credential and the current folder, can create Workspaces and Sessions, and can start a second service in the foreground. The one exception is `a-stack new --orchestrator NAME --no-browser --json` when the Operator asked you directly and nobody briefed you (see above); it never starts a service. If you were briefed, your Session already exists; check it with `me session inspect`.
- Success prints a `data` JSON object on stdout. Failure prints an `error` JSON object (`code`, `message`, `retryable`) on stderr and exits with 2 for `invalid_input` and 1 for all other errors. Check the exit code of each change.
- The Operator runs the service; leave it running. Never stop it, and never run `a-stack serve` on its state directory. To test a-stack itself, start a disposable service with a `mktemp -d` state directory and give `--state-dir` to every command. Changing `HOME` is not enough, because the Operator's `XDG_STATE_HOME` can still lead to the real state.
