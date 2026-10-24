# Getting started

This guide shows one complete a-stack workflow. You start a service with disposable state, enroll a small team, and let the Agents share a Task and its Task mail. Then you replace the Orchestrator, close and reopen the Session, and restore a backup.

The guide uses three roles:

- The Operator (you) creates the Workspace and the Session, registers Agents, appoints the Orchestrator, and closes or reopens the Session.
- The Orchestrator creates Tasks, assigns Agents to them and adds dependencies.
- The assignees are the Agents assigned to a Task. They update the Task, write its report and send its Task mail.

An Agent's runtime state (for example idle or done) is only an observation of its process. It does not change Task progress, and it does not set or clear a Needs you question.

## Before you start

You need Linux, Go 1.27.3, a C compiler and Python 3. Python is only for the small JSON helpers below.

Run the Bash blocks in order, in one shell, from the repository root. The blocks keep state and credentials in private temporary directories under `$TMPDIR` (or `/tmp` when `TMPDIR` is not set). These directories must be outside Git checkouts. For work that you want to keep, use durable absolute directories on a local filesystem, also outside Git checkouts.

a-stack accepts only private state and credential paths. They must belong to you and give no access to group or others, for example mode 0700 for directories and 0600 for files. The `umask 077` in Step 1 does this. Do not put credentials or mail bodies in shared logs. Do not turn on shell tracing (`set -x`).

## Step 1: Build and start the service

```bash
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
bin="$PWD/a-stack"
umask 077
state_dir="$(mktemp -d -t a-stack-state.XXXXXX)"
credential_dir="$(mktemp -d -t a-stack-credentials.XXXXXX)"

# serve usually runs in the foreground. Here it runs as one job of this shell.
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

Continue only when `health` succeeds. If it fails, read the private file `serve-error.json`. Do not start a second service on the same state directory.

No command installs a daemon. In normal use, run `serve` in one terminal. Run the other commands in a second terminal, with the same `--state-dir`.

## Step 2: Define the helpers

These helpers choose the credential explicitly and print only the JSON fields that you ask for:

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
  local capability_file="$1"
  shift
  "$bin" --state-dir "$state_dir" --as agent --credential-file "$capability_file" \
    --workspace "$workspace_id" --session "$session_id" --json "$@"
}
```

A successful command prints a `data` JSON document on stdout. A failed command prints an `error` JSON document on stderr and exits with a nonzero code. Read `error.code` to find the cause. Stop if an error is unexpected.

Use these helpers only with the state directory and the IDs of this example. Agent mode needs the Agent's own credential file. It never uses the Operator credential instead.

The blocks save each new ID in a variable. Do not paste IDs from another Session. IDs and revisions are ordinary metadata. They do not give access to anything.

## Step 3: Operator: create the team

Create one Workspace for the project folder. This example uses the repository root (`$PWD`). For your own project, use its absolute path. Then create a Session, register three Agents and appoint the first Agent as the Orchestrator:

```bash
workspace_id="$(operator workspace create --path "$PWD" \
  --operation-id demo-workspace-1 | field data.resource_id)"
session_id="$(operator session create --workspace "$workspace_id" --name Build \
  --operation-id demo-session-1 | field data.resource_id)"
orchestrator_credential="$credential_dir/orchestrator.json"
worker_a_credential="$credential_dir/worker-a.json"
worker_b_credential="$credential_dir/worker-b.json"
orchestrator_id="$(operator agent register --workspace "$workspace_id" --session "$session_id" \
  --name 01-orchestrator --new-credential-file "$orchestrator_credential" \
  --operation-id demo-register-orchestrator | field data.resource_id)"
worker_a_id="$(operator agent register --workspace "$workspace_id" --session "$session_id" \
  --name 02-worker --new-credential-file "$worker_a_credential" \
  --operation-id demo-register-worker-a | field data.resource_id)"
worker_b_id="$(operator agent register --workspace "$workspace_id" --session "$session_id" \
  --name 03-worker --new-credential-file "$worker_b_credential" \
  --operation-id demo-register-worker-b | field data.resource_id)"
session_revision="$(operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision)"
operator session appoint --workspace "$workspace_id" --session "$session_id" \
  --agent "$orchestrator_id" --expected-revision "$session_revision" --operation-id demo-appoint-1
```

The current directory never selects a Workspace or a Session. To link an Agent's separate worktree to the Workspace, add `--worktree ABSOLUTE_DIR` to `agent register`. The Orchestrator can also register Agents in its open Session, with its own Agent credential.

Give each Agent, in private, only what it needs: its own credential file, the binary and state directory locations, the Workspace, Session and Agent IDs, and the IDs of its Tasks. Do not put credential contents in prompts, reports, Task references or version control. In this guide, the separate Agents are simulated with their own credential files. For enrollment and credential recovery, see [Workspaces, Sessions and Agents](lifecycle.md).

## Step 4: Orchestrator: build the Task graph

The Orchestrator creates Task A and two dependent Tasks, B and C. It assigns two Agents to Task A. In the Task graph, A → B means that B depends on A.

```bash
task_a="$(as_agent "$orchestrator_credential" task create --title 'A: shared implementation' \
  --description 'Implement the shared change' --operation-id demo-task-a | field data.resource_id)"
task_b="$(as_agent "$orchestrator_credential" task create --title 'B: review' \
  --operation-id demo-task-b | field data.resource_id)"
task_c="$(as_agent "$orchestrator_credential" task create --title 'C: document' \
  --operation-id demo-task-c | field data.resource_id)"
task_revision="$(as_agent "$orchestrator_credential" task show --task "$task_a" | field data.revision)"
as_agent "$orchestrator_credential" task assign --task "$task_a" --agent "$worker_a_id" \
  --expected-revision "$task_revision" --operation-id demo-assign-a
task_revision="$(as_agent "$orchestrator_credential" task show --task "$task_a" | field data.revision)"
as_agent "$orchestrator_credential" task assign --task "$task_a" --agent "$worker_b_id" \
  --expected-revision "$task_revision" --operation-id demo-assign-b
graph_revision="$(as_agent "$orchestrator_credential" session inspect | field data.graph_revision)"
as_agent "$orchestrator_credential" dependency add --from "$task_a" --to "$task_b" \
  --expected-revision "$graph_revision" --operation-id demo-edge-ab
graph_revision="$(as_agent "$orchestrator_credential" session inspect | field data.graph_revision)"
as_agent "$orchestrator_credential" dependency add --from "$task_a" --to "$task_c" \
  --expected-revision "$graph_revision" --operation-id demo-edge-ac
as_agent "$worker_a_credential" task list
```

Each change needs the current revision of the thing that it changes:

- Dependency changes use the Session's `graph_revision`.
- Assignment changes and work updates use the Task's `revision`.
- Session close, reopen and appoint use the Session's lifecycle `revision`.

Only the current Orchestrator can create and edit Tasks, change assignments and change dependencies. Only the assignees can update a Task's work. The Operator cannot act as an assignee. To take part in Task mail, register a separate Agent and assign it to the Task. The Orchestrator also needs an assignment before it can read or send a Task's mail or update the Task's work.

Tasks B and C have no assignee. They stay visible as Unassigned. a-stack rejects a dependency that makes a cycle or connects two Sessions. When Task A is finished or cancelled, Tasks B and C do not change.

For all Task commands and paging, see [Tasks and the Task graph](tasks.md).

## Step 5: Agents: share mail and do the work

In a real Agent's shell, set `bin`, `state_dir`, `workspace_id`, `session_id`, `agent_id`, `agent_credential` and `task_id` from the private handoff. Copy only the `field` and `as_agent` helpers, and then define `worker` as below. The `worker` helper needs no Operator credential.

In this guide, the next block acts as Agent 02-worker:

```bash
# A real Agent uses the values from its own handoff.
agent_id="$worker_a_id"
agent_credential="$worker_a_credential"
task_id="$task_a"
worker() { as_agent "$agent_credential" "$@"; }
worker mail check --task-limit 50 --limit 100
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task status --task "$task_id" --progress working \
  --expected-revision "$task_revision" --operation-id demo-start-a
mail_receipt="$(worker mail send --task "$task_id" --body 'Ready to coordinate with @03-worker' \
  --operation-id demo-mail-a)"
as_agent "$worker_b_credential" mail send --task "$task_id" --body 'I will review the shared change' \
  --operation-id demo-mail-b
worker mail read --task "$task_id" --limit 100
as_agent "$worker_b_credential" mail read --task "$task_id" --limit 100
```

Both reads return the same messages. A read does not remove messages for the other Agent.

Agents check mail when they start or resume a Task, between large steps, and before they mark the whole Task finished. Mail checks work without Herdr.

A mention such as `@03-worker` must use the exact name of a current assignee. You can also name a recipient with `--mention NAME`. A mention is not a private message, and it does not give access to the Task. An Agent that joins or rejoins a Task can read the full mail history. When an Agent is removed from a Task, it can no longer read that Task's mail, and a-stack permanently suppresses its pending notices for that Task.

### Keep a cursor map

A cursor marks how far an Agent has read in one Task's mail. Each Agent keeps its own cursor map: a JSON object that maps Task IDs to cursors. The server does not store which messages an Agent has read, and the CLI does not write the map for you.

This block saves one response in a private file and merges the returned cursors into the map. The cursors of other Tasks stay in the map.

```bash
cursor_file="$credential_dir/worker-a-cursors.json"
printf '{}\n' >"$cursor_file"
worker mail check --cursor-file "$cursor_file" --task-limit 50 --limit 100 \
  >"$credential_dir/mail-check.json"
python3 - "$cursor_file" "$credential_dir/mail-check.json" <<'PY'
import json, pathlib, sys
path = pathlib.Path(sys.argv[1])
saved = json.loads(path.read_text())
response = json.loads(pathlib.Path(sys.argv[2]).read_text())["data"]
saved.update({page["task_id"]: page["cursor"] for page in response["tasks"]})
path.write_text(json.dumps(saved) + "\n")
PY
```

If the response has `data.next_task`, run the check again with the merged map and `--after-task` set to that value. Merge again, and repeat until `next_task` is not in the response. Then start the next round without `--after-task`. If a Task's page has `more: true`, read the rest in the next round, or with `mail read --task ID --cursor CURSOR`.

If an Agent loses its map or a cursor, the next read starts again from the first message. Delete temporary response files when you are done, because they contain shared mail.

A read returns 100 messages by default and 500 at most, and it also stops at a response size limit. `mail check` also limits the number of Tasks: 50 by default and 100 at most. A mail body must be valid UTF-8, must not be blank, and can be 64 KiB at most. a-stack rejects a larger body. It does not cut it. To send text from a file, use `--body-file FILE`, or `--body-file -` for stdin. Put large results in files and share a reference to them. For more about limits and continuation, see [Task mail](mail.md).

### Write the report and finish the Task

The next block writes a report, asks the Operator a question and resolves it, adds a file reference, and marks the Task finished. Each change reads the current Task revision first.

```bash
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task report --task "$task_id" --report 'Implementation and checks recorded' \
  --expected-revision "$task_revision" --operation-id demo-report-a
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task attention set --task "$task_id" --question 'Which release should include this?' \
  --expected-revision "$task_revision" --operation-id demo-question-a
# After the Operator answers in the existing conversation with the Agent:
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task attention resolve --task "$task_id" --note 'Operator selected the next release' \
  --expected-revision "$task_revision" --operation-id demo-resolve-a
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task reference add --task "$task_id" --location docs/results.md \
  --expected-revision "$task_revision" --operation-id demo-reference-a
worker mail check --cursor-file "$cursor_file"
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task status --task "$task_id" --progress finished --summary 'Whole shared Task completed' \
  --expected-revision "$task_revision" --operation-id demo-finish-a
```

An Agent marks a Task finished only when the whole shared Task is complete. The other assignees do not need to approve it. Viewing a Task or finishing it does not clear a Needs you question. Only `task attention resolve` clears it.

A file reference is a path or URL that people can see and copy. a-stack does not upload the file or read it. The reference in this example does not need to exist.

Reports and descriptions can be 64 KiB of UTF-8 at most. Results, reasons, questions and notes can be 2,048 bytes at most.

### Reopen or cancel a Task

Any current assignee can reopen a Finished or Cancelled Task. Reopening needs a reason and sets the Task to Waiting. Cancelling also needs a reason. Both keep the earlier results and the history.

```bash
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task reopen --task "$task_id" --reason 'Reconsider the approach' \
  --expected-revision "$task_revision" --operation-id demo-reopen-a
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task status --task "$task_id" --progress cancelled --reason 'Approach abandoned' \
  --expected-revision "$task_revision" --operation-id demo-cancel-a
```

## Retry after a lost response

Each change in this guide has an operation ID. Choose the ID before you send the change, and keep it. The `demo-*` IDs in this guide name the actions of this example. Use a new ID for each new action.

If you lose a response, send the same command again: the same payload, the same operation ID and the same expected revision. If the first request was committed and you still have access, the retry returns the original receipt and changes nothing. Do not change the payload or the expected revision under the same ID.

An error does not prove that an uncertain request was not committed. If a conflict confirms that your write is out of date, read the current state and think again about the change. Then send it with the current revision and a new operation ID.

`agent register` and `agent rebind` write the new private credential file before they send the request. If the outcome is uncertain, keep that file. Repeat the exact enrollment command, with the same credential and the same file name. Do not delete the file or make a new credential only because a response was lost. For confirmed rejections and lost tokens, see [credential recovery](lifecycle.md#credential-delivery-and-recovery).

## Replace the Orchestrator

When the Orchestrator is absent, the other Agents continue their assignee work. Graph and assignment changes wait for an Orchestrator. A runtime state of idle, done or unknown does not finish Tasks, clear questions, choose a new Orchestrator or close the Session.

The Operator can appoint another Agent of the same Session:

```bash
session_revision="$(operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision)"
operator session appoint --workspace "$workspace_id" --session "$session_id" \
  --agent "$worker_b_id" --expected-revision "$session_revision" --operation-id demo-replace-orchestrator
```

The old Orchestrator loses its Orchestrator authority immediately. It keeps the authority of an ordinary assignee.

A new Agent attaches its own runtime. To replace or clear a runtime binding that is already set, the Operator runs `agent rebind` with the current Agent revision and a different `--new-credential-file`. This rotates the credential and makes the old binding and token invalid. Use `agent list` to see Agent revisions. A binding with only `--runtime-id` stays unverified and never gets automatic notices. For examples, see [runtime bindings](lifecycle.md#runtime-bindings).

## Close and reopen the Session

You can close a Session that has unfinished Tasks. Closing keeps all statuses and history, and authorized mail reads continue to work. It pauses pending notices and rejects new work updates and new mail. It does not stop Agent processes. Reopening restores the normal permissions. It does not change Task statuses or start Agents.

```bash
session_revision="$(operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision)"
operator session close --workspace "$workspace_id" --session "$session_id" \
  --expected-revision "$session_revision" --operation-id demo-close-session
worker mail read --task "$task_id" --limit 100
session_revision="$(operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision)"
operator session reopen --workspace "$workspace_id" --session "$session_id" \
  --expected-revision "$session_revision" --operation-id demo-reopen-session
```

## Look at the work

The Operator and the registered Agents can read metadata with `task list`, `task show`, `session inspect` and `agent list`.

`open --workspace ID --session ID` gives a read-only browser URL for one Session. Add `--no-browser` to print the URL without opening a browser. Keep the URL private, because it works like a credential. The browser view cannot read Task mail or change anything. a-stack never updates or closes GitHub tickets. For details, see [Browser view](browser.md).

## Back up, restore and stop

For work that you keep, store completed backups and the original credential files in private, durable storage, separate from each other. A database backup contains only hashes of the credentials. It cannot restore the tokens. Keep every Agent credential file that you gave out, and the Operator file. A token that was rotated out stays invalid after a restore. After a restore, check the database ID, the retained work and mail, and a known retry receipt. Only then retire the old state directory.

This block makes a backup with a new name, and stops only the service of this example. It then restores the backup into a new empty directory and checks it:

```bash
database_id="$("$bin" health --state-dir "$state_dir" --json | field data.database_id)"
backup_dir="$(mktemp -d -t a-stack-backup.XXXXXX)"
operator storage backup --destination "$backup_dir/completed.sqlite"
install -m 600 "$state_dir/operator.json" "$backup_dir/operator.json"
# Keep credential_dir separately. It holds the three original Agent credential files.
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
restored_database_id="$("$bin" health --state-dir "$state_dir" --json | field data.database_id)"
test "$restored_database_id" = "$database_id"
worker task show --task "$task_id"
worker mail read --task "$task_id" --limit 100
restored_receipt="$(worker mail send --task "$task_id" --body 'Ready to coordinate with @03-worker' \
  --operation-id demo-mail-a)"
test "$restored_receipt" = "$mail_receipt"
kill -TERM "$service_pid"
wait "$service_pid"
trap - EXIT
```

The service makes the SQLite backup and checks its integrity before the backup file appears. It refuses a destination that already exists. If a backup is interrupted, check whether the destination file is complete, or use a new name.

Never copy only a live `state.db`. Never delete the SQLite sidecar files, overwrite an existing backup, or mix a restored database with old sidecar files. For details, see [backup and restore](foundation.md#backup-and-restore).

When the service is stopped and you no longer need the files, you can delete the temporary directories of this example. Real state and credentials need a deliberate backup plan.

## Optional mention notices

Mail checks work without Herdr. To turn on notices, start the service with `--herdr-executable /absolute/path/to/herdr`, and enroll complete private bindings as described in [Mention notices](notifications.md).

Only one combination is supported: native Codex 0.164.0 with Herdr 0.9.5. The declared harness version and the full current identity of the target (alias, pane, terminal and native session) must match. All other targets stay mail-only, for example unsupported, unknown, blocked or stopped targets. When you rebind or upgrade an Agent, keep the declared version correct.

A notice is one generic message that tells the Agent to check its own mail. a-stack does not wait for the Agent to be idle, and it does not start or restart Agents. Herdr cannot guarantee that the same Agent is still in the pane between the check and the input. A submitted notice does not prove that the Agent read the mail or acted on it. a-stack never resends a notice with an uncertain outcome automatically. New mentions can start a new batch.

This guide does not need live notices. For release evidence and its limits, see [Validation](validation.md).
