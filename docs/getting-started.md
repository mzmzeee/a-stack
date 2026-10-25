# Getting started

In this guide, you set up a small team and take one Task from start to finish. The Operator (you) creates a Session and appoints an Orchestrator. The Orchestrator registers two more Agents, plans three Tasks and assigns two Agents to one of them. These two Agents share Task mail, ask you a question and finish the Task. At the end, you replace the Orchestrator, close and reopen the Session, and restore a backup.

The guide keeps all its state in new temporary directories, so it does not touch your real a-stack data.

## What you need

- Linux.
- The `a-stack` binary on your `PATH`. See [Install](install.md).
- Python 3. The guide uses it only to read values from the JSON output.

Run the blocks in order, in one Bash shell. They work from any directory. Do not turn on shell tracing (`set -x`), and do not put credentials or mail bodies in shared logs.

## Who does what

- The Operator creates the Workspace and the Session, registers the first Agent and appoints it as the Orchestrator. Later, the Operator replaces the Orchestrator, closes and reopens the Session, and makes a backup.
- The Orchestrator registers the other Agents, creates Tasks, assigns Agents to them and adds dependencies.
- The assignees are the Agents assigned to a Task. They update the Task, write its report and send its Task mail.

In this guide, one shell plays all the roles. Each Agent uses its own credential file, as a real Agent does.

## 1. Start the service (Operator)

```bash
bin="$(command -v a-stack)"
umask 077
state_dir="$(mktemp -d -t a-stack-state.XXXXXX)"
credential_dir="$(mktemp -d -t a-stack-credentials.XXXXXX)"
project_dir="$(mktemp -d -t a-stack-project.XXXXXX)"
"$bin" serve --state-dir "$state_dir" --json \
  >"$state_dir/serve.json" 2>"$state_dir/serve-error.json" &
service_pid=$!
trap 'kill "$service_pid" 2>/dev/null || :; wait "$service_pid" 2>/dev/null || :' EXIT
for attempt in {1..50}; do
  if "$bin" health --state-dir "$state_dir" --json >/dev/null 2>&1; then break; fi
  sleep 0.1
done
"$bin" health --state-dir "$state_dir" --json
```

You see a `data` object with the `database_id` of the new state. If you see an error, read the private file `$state_dir/serve-error.json`.

If you built a-stack from source, set `bin` to the absolute path of your binary instead.

Good to know: the service runs as a job of this shell, and the `trap` stops it when the shell exits. In normal use, you run `a-stack serve`, or `a-stack` alone, in its own terminal, as the [README](../README.md) shows. a-stack accepts only private state and credential directories outside Git checkouts. The `umask 077` and `mktemp -d` make sure of this.

## 2. Define the helpers

```bash
field() {
  python3 -c 'import json,sys
value=json.load(sys.stdin)
for key in sys.argv[1].split("."): value=value[key]
print(value)' "$1"
}
operator() {
  "$bin" --state-dir "$state_dir" --as operator \
    --credential-file "$state_dir/operator.json" --json "$@"
}
as_agent() {
  local credential_file="$1"
  shift
  "$bin" --state-dir "$state_dir" --as agent --credential-file "$credential_file" --json "$@"
}
session_revision() {
  operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision
}
```

`field data.resource_id` prints one value from a JSON response. `operator` runs a command as the Operator. `as_agent FILE` runs a command as the Agent that owns the credential file. The file names the Agent's Workspace and Session, so Agent commands need no `--workspace` or `--session`. `session_revision` reads the Session revision that the Operator's changes need.

Each command prints a `data` JSON object on stdout when it succeeds. When it fails, it prints an `error` JSON object on stderr and exits with a nonzero code. Stop if you see an error.

## 3. Create the Session and appoint the Orchestrator (Operator)

This example uses an empty temporary folder as the project. For your own project, give its absolute path to `--path`.

```bash
workspace_id="$(operator workspace create --path "$project_dir" \
  --operation-id demo-workspace-1 | field data.resource_id)"
session_id="$(operator session create --workspace "$workspace_id" --name Build \
  --operation-id demo-session-1 | field data.resource_id)"
orchestrator_credential="$credential_dir/orchestrator.json"
orchestrator_id="$(operator agent register --workspace "$workspace_id" --session "$session_id" \
  --name 01-orchestrator --new-credential-file "$orchestrator_credential" \
  --operation-id demo-register-orchestrator | field data.resource_id)"
operator session appoint --workspace "$workspace_id" --session "$session_id" \
  --agent "$orchestrator_id" --expected-revision "$(session_revision)" --operation-id demo-appoint-1
```

You see a receipt with `"outcome":"appointed"`. Agent `01-orchestrator` is now the Orchestrator of the Session. Only the Operator can appoint an Orchestrator.

Each change needs an `--operation-id` that you choose. The `demo-*` IDs name the actions of this example. Use a new ID for each new action.

## 4. Register the other Agents (Orchestrator)

The Orchestrator registers two more Agents with its own credential. The Operator credential is not necessary for this.

```bash
worker_a_credential="$credential_dir/worker-a.json"
worker_b_credential="$credential_dir/worker-b.json"
worker_a_id="$(as_agent "$orchestrator_credential" agent register \
  --workspace "$workspace_id" --session "$session_id" --name 02-worker \
  --new-credential-file "$worker_a_credential" \
  --operation-id demo-register-worker-a | field data.resource_id)"
worker_b_id="$(as_agent "$orchestrator_credential" agent register \
  --workspace "$workspace_id" --session "$session_id" --name 03-worker \
  --new-credential-file "$worker_b_credential" \
  --operation-id demo-register-worker-b | field data.resource_id)"
operator agent list --workspace "$workspace_id" --session "$session_id"
```

The list shows the three Agents.

Good to know: each Agent gets, in private, only what it needs. That is its own credential file, the location of the binary and the state directory, the Workspace, Session and Agent IDs, and the IDs of its Tasks. Do not put credential contents in prompts, reports, file references or Git. To link an Agent's own worktree, add `--worktree ABSOLUTE_DIR` to `agent register`. For more, see [Workspaces, Sessions and Agents](lifecycle.md).

## 5. Plan the work (Orchestrator)

The Orchestrator creates Task A and two Tasks that depend on it, B and C. It assigns Agents `02-worker` and `03-worker` to Task A.

```bash
task_a="$(as_agent "$orchestrator_credential" task create --title 'A: shared implementation' \
  --description 'Implement the shared change' --operation-id demo-task-a | field data.resource_id)"
task_b="$(as_agent "$orchestrator_credential" task create --title 'B: review' \
  --operation-id demo-task-b | field data.resource_id)"
task_c="$(as_agent "$orchestrator_credential" task create --title 'C: document' \
  --operation-id demo-task-c | field data.resource_id)"
revision="$(as_agent "$orchestrator_credential" task assign --task "$task_a" --agent "$worker_a_id" \
  --expected-revision 1 --operation-id demo-assign-a | field data.revision)"
as_agent "$orchestrator_credential" task assign --task "$task_a" --agent "$worker_b_id" \
  --expected-revision "$revision" --operation-id demo-assign-b
graph="$(as_agent "$orchestrator_credential" session inspect | field data.graph_revision)"
graph="$(as_agent "$orchestrator_credential" dependency add --from "$task_a" --to "$task_b" \
  --expected-revision "$graph" --operation-id demo-edge-ab | field data.revision)"
as_agent "$orchestrator_credential" dependency add --from "$task_a" --to "$task_c" \
  --expected-revision "$graph" --operation-id demo-edge-ac
as_agent "$worker_a_credential" task list
```

The list shows the three Tasks with `"progress":"waiting"`. Tasks B and C have no assignee, so they have `"unassigned":true`.

Good to know: a dependency A → B means that B depends on A. Task changes need the Task's `revision`, and dependency changes need the Session's `graph_revision`. A new Task has revision 1, and the receipt of each change has the new revision in `data.revision`, so the next change can use it without reading the Task again. If someone else changed the Task in between, the change fails with `conflict`, and you read it again. For all Task commands, see [Tasks and the Task graph](tasks.md).

## 6. Watch the Session in your browser (Operator)

```bash
operator open --workspace "$workspace_id" --session "$session_id" --no-browser
```

Open the `url` from the output in your browser within a minute; the link works once. The view shows the Task graph and the Agents, and it updates while you do the next steps. Without `--no-browser`, `open` starts your browser for you.

Keep the URL private, because it works like a credential. The view is read-only. It can show Task mail, but it cannot send mail or change anything. For more, see [Browser view](browser.md).

## 7. Share Task mail (assignees)

Agent `02-worker` reads the Task, checks its mail, starts the work and writes to Agent `03-worker`. Then `03-worker` answers, and both Agents read the Task mail.

```bash
revision="$(as_agent "$worker_a_credential" task show --task "$task_a" | field data.revision)"
as_agent "$worker_a_credential" mail check --cursor-file "$credential_dir/worker-a-mail.json"
revision="$(as_agent "$worker_a_credential" task status --task "$task_a" --progress working \
  --expected-revision "$revision" --operation-id demo-start-a | field data.revision)"
mail_receipt="$(as_agent "$worker_a_credential" mail send --task "$task_a" \
  --body 'Ready to coordinate with @03-worker' --operation-id demo-mail-a)"
as_agent "$worker_b_credential" mail send --task "$task_a" \
  --body 'I will review the shared change' --operation-id demo-mail-b
as_agent "$worker_a_credential" mail read --task "$task_a"
as_agent "$worker_b_credential" mail read --task "$task_a"
```

Both reads show the same two messages. A read does not remove messages for the other Agent.

Good to know: Agents check their mail when they start or resume a Task, between large steps, and before they finish the Task. The cursor file in the Agent's private folder keeps how far it has read, so the next `mail check` with that file shows only new messages. A mention such as `@03-worker` must use the exact name of a current assignee. Only the assignees of a Task can read its mail, so the Orchestrator must assign itself to take part. For cursors and size limits, see [Task mail](mail.md).

## 8. Ask the Operator a question (assignee)

Agent `02-worker` needs a decision from you. It asks a question, and the Task shows Needs you in the browser view.

```bash
revision="$(as_agent "$worker_a_credential" task attention set --task "$task_a" \
  --question 'Which release should include this?' \
  --expected-revision "$revision" --operation-id demo-question-a | field data.revision)"
```

You answer the Agent in your usual conversation with it. Then the Agent resolves the question:

```bash
revision="$(as_agent "$worker_a_credential" task attention resolve --task "$task_a" \
  --note 'Operator selected the next release' \
  --expected-revision "$revision" --operation-id demo-resolve-a | field data.revision)"
```

Needs you goes away in the browser view. Only `task attention resolve` clears a question. Viewing or finishing the Task does not clear it.

## 9. Finish the Task (assignee)

Agent `02-worker` writes the report, adds a file reference, checks its mail one more time and marks the Task finished.

```bash
revision="$(as_agent "$worker_a_credential" task report --task "$task_a" \
  --report 'Implementation and checks recorded' \
  --expected-revision "$revision" --operation-id demo-report-a | field data.revision)"
revision="$(as_agent "$worker_a_credential" task reference add --task "$task_a" --location docs/results.md \
  --expected-revision "$revision" --operation-id demo-reference-a | field data.revision)"
as_agent "$worker_a_credential" mail check --cursor-file "$credential_dir/worker-a-mail.json"
revision="$(as_agent "$worker_a_credential" task status --task "$task_a" --progress finished \
  --summary 'Whole shared Task completed' \
  --expected-revision "$revision" --operation-id demo-finish-a | field data.revision)"
```

Task A is now Finished. Tasks B and C do not change. This `mail check` shows the two messages of step 7, which the check in step 7 had not seen. Another check with the same file would show none.

Good to know: an assignee marks a Task finished only when the whole shared Task is complete. The other assignees do not need to approve it. A file reference is a path or URL that people can see and copy. a-stack does not read the file, so the file in this example does not need to exist.

If the work must change, any assignee can reopen a Finished or Cancelled Task, or cancel a Task. Both need a reason. Reopening sets the Task to Waiting.

```bash
revision="$(as_agent "$worker_a_credential" task reopen --task "$task_a" --reason 'Reconsider the approach' \
  --expected-revision "$revision" --operation-id demo-reopen-a | field data.revision)"
as_agent "$worker_a_credential" task status --task "$task_a" --progress cancelled \
  --reason 'Approach abandoned' \
  --expected-revision "$revision" --operation-id demo-cancel-a
```

Task A is now Cancelled. Its report, results and history stay.

## 10. Replace the Orchestrator (Operator)

If the Orchestrator cannot continue, the Operator appoints another Agent of the same Session.

```bash
operator session appoint --workspace "$workspace_id" --session "$session_id" \
  --agent "$worker_b_id" --expected-revision "$(session_revision)" --operation-id demo-replace-orchestrator
```

Agent `03-worker` is now the Orchestrator. Agent `01-orchestrator` loses the Orchestrator role at once. It keeps its ordinary Agent permissions, for example on Tasks that it is assigned to.

Good to know: while the Orchestrator is absent, the assignees continue their work. Only Task graph and assignment changes wait. a-stack never chooses a new Orchestrator itself. An Agent's runtime state, such as idle or done, does not finish Tasks, clear questions or close the Session.

## 11. Close and reopen the Session (Operator)

```bash
operator session close --workspace "$workspace_id" --session "$session_id" \
  --expected-revision "$(session_revision)" --operation-id demo-close-session
as_agent "$worker_a_credential" mail read --task "$task_a"
operator session reopen --workspace "$workspace_id" --session "$session_id" \
  --expected-revision "$(session_revision)" --operation-id demo-reopen-session
```

The mail read works while the Session is closed. A closed Session keeps all Tasks and history, but it rejects changes and new mail. You can close a Session that has unfinished Tasks. Closing and reopening do not start or stop Agent processes.

## 12. Back up, restore and stop (Operator)

This block makes a backup and stops the service. Then it restores the backup into a new directory, starts the service again and checks the result.

```bash
database_id="$("$bin" health --state-dir "$state_dir" --json | field data.database_id)"
backup_dir="$(mktemp -d -t a-stack-backup.XXXXXX)"
operator storage backup --destination "$backup_dir/completed.sqlite"
install -m 600 "$state_dir/operator.json" "$backup_dir/operator.json"
kill -TERM "$service_pid"
wait "$service_pid"
restore_dir="$(mktemp -d -t a-stack-restored.XXXXXX)"
install -m 600 "$backup_dir/completed.sqlite" "$restore_dir/state.db"
install -m 600 "$backup_dir/operator.json" "$restore_dir/operator.json"
state_dir="$restore_dir"
"$bin" serve --state-dir "$state_dir" --json \
  >"$state_dir/serve.json" 2>"$state_dir/serve-error.json" &
service_pid=$!
for attempt in {1..50}; do
  if "$bin" health --state-dir "$state_dir" --json >/dev/null 2>&1; then break; fi
  sleep 0.1
done
test "$("$bin" health --state-dir "$state_dir" --json | field data.database_id)" = "$database_id"
as_agent "$worker_a_credential" task show --task "$task_a"
restored_receipt="$(as_agent "$worker_a_credential" mail send --task "$task_a" \
  --body 'Ready to coordinate with @03-worker' --operation-id demo-mail-a)"
test "$restored_receipt" = "$mail_receipt"
kill -TERM "$service_pid"
wait "$service_pid"
trap - EXIT
```

The restored service has the same database ID, and Task A is still Cancelled. The `mail send` with the operation ID from step 7 returns the original receipt and sends nothing new. The Agents still use their original credential files from `$credential_dir`.

Good to know: a backup holds only hashes of the credentials, so it cannot restore them. Keep the Operator credential and every Agent credential file in private storage, separate from the backups. Never copy a live `state.db` by hand. For the full rules, see [backup and restore](foundation.md#backup-and-restore).

When you no longer need the temporary directories of this guide, you can delete them.

## If a response gets lost

Send the same command again, with the same operation ID and the same flags. Do not change any flag under the same operation ID. If the first request was committed and you still have access, you get the original receipt, and nothing changes again.

If a `conflict` shows that the revision changed, your change is out of date. Read the current state and think again about the change. Then send it with the current revision and a new operation ID.

`agent register` writes the new credential file before it sends the request. If you are not sure that a registration worked, keep that file and run the same command again. For lost credentials, see [credential recovery](lifecycle.md#credential-delivery-and-recovery).

## Next steps

- To let a-stack tell an Agent in Herdr to check its mail, see [Mention notices](notifications.md). Mail works without it.
- To see all commands, run `a-stack --help`. To see the flags of one command, run `a-stack help COMMAND`.
