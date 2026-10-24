# Getting started: Operator and Agent workflow

a-stack tracks a Workspace's Sessions, shared Tasks and Task mail. The Operator enrolls the team and manages Session lifecycle; the Session's Orchestrator controls its graph and assignments. Current assignees update work and exchange mail. Runtime state is an observation, separate from Task progress and an explicit **Needs you** question.

## Build and start a disposable example

Use Linux, Go **1.27.3**, a C compiler and Python 3 for the JSON extraction below. Run these Bash blocks in order, in the same shell, from the repository root. The example uses private `/tmp` directories; for retained work choose durable absolute directories on a local filesystem outside Git checkouts. State directories must be 0700 and credential files 0600. Keep credentials and mail bodies out of shared logs; leave shell tracing off.

```bash
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
bin="$PWD/a-stack"
umask 077
state_dir="$(mktemp -d /tmp/a-stack-state.XXXXXX)"
credential_dir="$(mktemp -d /tmp/a-stack-credentials.XXXXXX)"

# serve normally runs in the foreground. This example owns one shell job.
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

Continue only after health succeeds. On failure inspect the private `serve-error.json`; do not start another service against the same state directory. No command installs a daemon. For normal foreground use, run `serve` in one terminal and pass the same `--state-dir` to clients in another.

These small helpers select authority explicitly and extract only named JSON fields. IDs and revisions are ordinary metadata; they are not bearer credentials. Substitute your real project path for `$PWD` if needed. Later ID variables are captured automatically; do not paste sample IDs from another Session.

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

Success is a `data` JSON document on stdout. Errors use stderr, a nonzero exit and an `error.code`; stop on an unexpected error. Use these helpers only with the example's selected state directory and context. Agent mode requires its own credential file and never falls back to the Operator.

## Operator: enroll and appoint the team

Create one Workspace for the project folder, then a Session and three registrations. `--worktree ABSOLUTE_DIR` may explicitly associate an Agent's worktree with this Workspace; changing cwd never selects context.

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

Privately give each Agent **only its own** credential file, binary/State location, Workspace/Session/Agent IDs and assigned Task IDs. Do not put credential contents in prompts, reports, Task references or version control. The examples below simulate those separate callers using their explicit files. An Orchestrator may also register workers using its own Agent credential in its open Session. See [enrollment and credential recovery](lifecycle.md).

## Orchestrator: create and assign a branching graph

Create A with dependents B and C. **A → B means B depends on A.** Dependency edits use the Session's `graph_revision`; assignment and work edits use the Task's `revision`. Session close/appoint uses the separate lifecycle `revision`.

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

Only the current Orchestrator creates/edits Task definitions, assignments and dependencies. Assignees own work updates. The Operator cannot perform assignee actions; to participate in Task mail, use a separately registered, assigned Agent's credential. An Orchestrator also needs assignment before reading/sending a Task's mail or updating its work. Unassigned B/C remain visible until assigned. Cycles and cross-Session edges are rejected; completion/cancellation of A never silently changes B/C. [Task and graph contracts](tasks.md) cover paging and all commands.

## Agents: shared mail and work

In an Agent's shell, set `bin`, `state_dir`, `workspace_id`, `session_id`, its own `agent_id`, `agent_credential` and assigned `task_id` from the private handoff. Copy just the `field` and `as_agent` helper definitions above, then define `worker` below. This wrapper needs no Operator credential:

```bash
# For this walkthrough, act as worker A. A real Agent uses its handed-off values.
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

Both reads return the same retained history; neither consumes the other's messages. Check mail at start/resume, between major steps and before whole-Task completion. A mention targets an exact current-assignee name (or `--mention NAME`), not a private DM or an access grant. Ordinary checks work with no Herdr. A joining/rejoining assignee gets full history; removal ends access and permanently suppresses its old pending notices.

Save each caller's own cursor map. This example saves a bounded response privately and merges returned positions without losing other Tasks' cursors:

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

The CLI does not update the map itself. If `data.next_task` exists, repeat the check with the merged map and `--after-task` set to that token, merging again until absent. Then begin the next round without `--after-task`; revisit any `more: true` page in that round or with `mail read --task ID --cursor CURSOR`. Each Agent keeps a separate map. A lost map/cursor honestly replays history; no server unread/read ledger exists. Remove temporary response files when finished; they contain shared mail.

Reads default to 100 messages, cap at 500 and obey a response-byte budget; `mail check` also bounds Tasks (default 50, max 100). Bodies must be nonblank UTF-8, at most 64 KiB, and are rejected rather than truncated. Use `--body-file FILE` or `--body-file -` for mail/report text; keep large results in files and share references. See [mail limits and continuation](mail.md).

Record reports, Operator questions and file-reference metadata with fresh current Task revisions:

```bash
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task report --task "$task_id" --report 'Implementation and checks recorded' \
  --expected-revision "$task_revision" --operation-id demo-report-a
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task attention set --task "$task_id" --question 'Which release should include this?' \
  --expected-revision "$task_revision" --operation-id demo-question-a
# After the Operator answers through the existing Agent conversation:
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

Only mark Finished when the **whole shared Task** is complete; no peer approval is required. Viewing/finishing never clears Needs you. References are display/copy paths or URLs, not uploads or server-read files. The example's reference need not exist. Reports/descriptions allow 64 KiB UTF-8; result/reason/question/note text allows 2,048 bytes.

Any current assignee can reopen Finished or Cancelled to Waiting with a reason; cancellation also requires a reason. Both retain earlier results/history:

```bash
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task reopen --task "$task_id" --reason 'Reconsider the approach' \
  --expected-revision "$task_revision" --operation-id demo-reopen-a
task_revision="$(worker task show --task "$task_id" | field data.revision)"
worker task status --task "$task_id" --progress cancelled --reason 'Approach abandoned' \
  --expected-revision "$task_revision" --operation-id demo-cancel-a
```

## Retries, replacement and Session closure

Choose and retain an operation ID before each mutation. The `demo-*` IDs above identify this example's actions; use a new ID for genuinely new work. If a response is lost, repeat the **same command/payload/operation ID**, including its original expected revision. An authorized committed retry returns its original receipt without another effect. Do not change the expected revision or payload under that ID. On a confirmed stale-write conflict, inspect current work, reconsider it, then use the current revision and a new ID for the new action. Errors do not prove an ambiguous request failed to commit.

Registration/rebind writes the new private credential file before the request. On an uncertain outcome keep that file and repeat the exact enrollment command, including actor and filename. Do not delete it or generate another credential merely because a response was lost. [Credential recovery](lifecycle.md#credential-delivery-and-recovery) explains confirmed rejection and lost-token recovery.

Workers continue ordinary assignee work while the Orchestrator is absent; graph/assignment changes wait. Runtime idle/done/unknown does not complete Tasks, clear questions, elect another Orchestrator or close a Session. The Operator can appoint an existing same-Session Agent explicitly:

```bash
session_revision="$(operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision)"
operator session appoint --workspace "$workspace_id" --session "$session_id" \
  --agent "$worker_b_id" --expected-revision "$session_revision" --operation-id demo-replace-orchestrator
```

The old holder immediately loses Orchestrator authority while retaining any ordinary assignee authority. A fresh Agent attaches its own runtime; replacing/clearing an established binding requires Operator `agent rebind` with its current Agent revision and a different `--new-credential-file`, rotating the credential and invalidating the old binding/token. Use `agent list` for Agent revisions. Instance-only `--runtime-id` bindings remain unverified and enable no automatic notice; see [runtime attachment and rebind examples](lifecycle.md#runtime-bindings).

Closing unfinished work is allowed. It preserves statuses/history and authorized mail reads, pauses pending notices and rejects new work/mail writes. It never terminates Agent processes. Explicit reopen restores ordinary permissions without changing statuses or starting agents:

```bash
session_revision="$(operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision)"
operator session close --workspace "$workspace_id" --session "$session_id" \
  --expected-revision "$session_revision" --operation-id demo-close-session
worker mail read --task "$task_id" --limit 100
session_revision="$(operator session inspect --workspace "$workspace_id" --session "$session_id" | field data.revision)"
operator session reopen --workspace "$workspace_id" --session "$session_id" \
  --expected-revision "$session_revision" --operation-id demo-reopen-session
```

Operator/registered Agents can inspect metadata with `task list/show`, `session inspect` and `agent list`. `open --workspace ID --session ID` supplies a scoped read-only browser URL; `--no-browser` prints it without launching. Treat that URL as a credential. Browser access grants no Task mail or mutation authority. Nothing automatically updates/closes GitHub tickets. See [the browser guide](browser.md).

## Backup, restore and stop

Keep completed backups and original credential files in separate private durable storage for retained work. A database backup contains capability hashes, not recoverable tokens. Preserve all delivered Agent credential files as well as the Operator file; a rotated-out token stays invalid. Check database identity, retained work/mail and a known retry receipt after restoration before retiring the old state directory.

For this disposable example, use a fresh backup name, stop only its owned service, and restore into a new empty directory:

```bash
database_id="$("$bin" health --state-dir "$state_dir" --json | field data.database_id)"
backup_dir="$(mktemp -d /tmp/a-stack-backup.XXXXXX)"
operator storage backup --destination "$backup_dir/completed.sqlite"
install -m 600 "$state_dir/operator.json" "$backup_dir/operator.json"
# Keep credential_dir separately; it includes the three original Agent files.
kill -TERM "$service_pid"
wait "$service_pid"
restore_dir="$(mktemp -d /tmp/a-stack-restored.XXXXXX)"
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

The service creates and integrity-checks the SQLite backup before publishing it; an existing destination is refused. After an interrupted backup inspect the completed destination or use a new name. Never copy only a live `state.db`, delete SQLite sidecars, overwrite an existing backup or merge a restore with old sidecars. See [backup/restore details](foundation.md#backup-and-restore). The temporary example directories may be removed once their service is stopped and their contents are no longer needed; real retained state and credentials require deliberate preservation.

## Optional mention notices and validation

Mail checks above work without Herdr. To opt in, start the service with `--herdr-executable /absolute/path/to/herdr` and enroll complete private bindings following [notification setup](notifications.md). Only **native Codex 0.164.0 / Herdr 0.9.5** is enabled. Declared harness version and complete current alias/pane/terminal/native-session identity must match; unsupported/unknown/blocked/stopped targets remain mail-only. Rebinding/upgrading must keep the declaration accurate.

The normal queue sends one generic check-your-own-mail notice, with no idle wait or agent launch/restart. Herdr cannot atomically pin the occupant between lookup and input. Successful submission is not proof of reading/action; uncertain submissions are never automatically resent, while new mentions may form a fresh batch. No live notices are needed for this walkthrough. See [reproducible validation and measurement procedures](validation.md) for release evidence and its limits.
