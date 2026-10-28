# Tasks and the Task graph

A Task belongs to one Session. The Operator names the Session in each Task command with `--workspace ID --session ID`. An Agent runs Task commands with `--as agent --credential-file PRIVATE_FILE`. The Agent can leave both IDs out: the CLI takes them from the credential file, or for a file from before v0.1.5 from the service (see [Credential context](lifecycle.md#credential-context)). The current directory never changes the context. (Only the Operator commands `a-stack`, `a-stack -r` and `a-stack new` use it, to choose a Session to open. See [Workspaces, Sessions and Agents](lifecycle.md).) Before you use these commands, enroll the Operator, the Orchestrator and the other Agents as described in [Workspaces, Sessions and Agents](lifecycle.md).

## Who can do what

The current Orchestrator defines the work. It creates Tasks, edits their title, description and Ticket reference, assigns and unassigns Agents, and adds and removes dependencies.

The current assignees do the work. An assignee is an Agent that is assigned to the Task. Assignees update the progress, the report, the file references and the Operator questions.

The Operator answers Needs you questions. The Orchestrator must assign itself to a Task before it can do assignee actions on it. The Operator never acts as an assignee. When the Operator replaces the Orchestrator, the old Agent loses its graph authority immediately, but it keeps its assignee authority. A runtime state of unknown, idle or done changes no authority and no Task progress.

| Command | Who | Purpose |
| --- | --- | --- |
| `task create` | Orchestrator | Create a Task. |
| `task edit` | Orchestrator | Change the title, description or Ticket reference. |
| `task assign`, `task unassign` | Orchestrator | Add or remove an assignee. |
| `dependency add`, `dependency remove` | Orchestrator | Add or remove a dependency. |
| `task status`, `task reopen` | Assignee | Change the progress. |
| `task archive`, `task unarchive` | Orchestrator or Operator | Archive a Finished or Cancelled Task, or undo it. |
| `task report` | Assignee | Replace the shared report. |
| `task attention set`, `task attention resolve` | Assignee | Ask or resolve an Operator question. |
| `answer` | Operator | Answer the open Operator question. The assignees get the answer as Task mail. |
| `task reference add`, `task reference remove` | Assignee | Add or remove a file reference. |
| `task list`, `task show`, `task history`, `task assignees`, `task references`, `task dependencies`, `dependency history`, `session snapshot`, `session page` | Operator or Session Agent | Read Tasks and the Task graph. |

## Create and assign a Task

These examples use the IDs and private credentials from [Workspaces, Sessions and Agents](lifecycle.md). If you use a different state directory, add the same `--state-dir` to every command.

```sh
a-stack task create --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --title "Build feature" \
  --body-file work-description.txt --ticket '#42' --operation-id task-create-42 --json
# Set task_id to data.resource_id. A new Task has revision 1.
a-stack task assign --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --agent "$worker_a_id" --expected-revision 1 --operation-id assign-worker-a --json
a-stack task assign --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --agent "$worker_b_id" --expected-revision 2 --operation-id assign-worker-b --json
a-stack task status --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --progress working --expected-revision 3 --operation-id task-start-work --json
```

`task create` can link and assign the new Task in the same operation. Give `--after TASK_ID` once for each prerequisite and `--assign AGENT` once for each assignee. a-stack adds the dependencies and the assignments in the same transaction as the Task, or nothing when one of them is wrong, so a retry with the same operation ID is safe. A new Task has no dependents and nobody has read its revision yet, so these links need no expected revision, and no `session inspect` before them. The dependencies take the graph revision of the create. Each assignment is one Task revision with its own `task.assign` history entry, so the receipt's revision is 1 plus the number of assignees. The example above in one command:

```sh
a-stack task create --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --title "Build feature" \
  --after "$design_task_id" --assign 02-worker --assign 03-worker \
  --operation-id task-create-42 --json
# data.revision is 3: the Task and its two assignments.
```

`--assign`, and `--agent` in every command that takes it except the hook commands, takes an Agent ID or the Agent's name. The CLI looks the name up with `agent list` in the Session and sends the ID. Names are unique in a Workspace; an unknown name fails before any change. A create without `--after` in a Session that has other open (Waiting or Working) Tasks prints one hint line on stderr, such as `a-stack: hint: the new Task has no prerequisites, and 3 other Task(s) are open; …`. stdout keeps only the receipt.

Both assignees see the same Task ID, revision and status. `task show` reads the current details of one Task. `task list` shows all Tasks, also the Unassigned ones. Use the revision from the last response for the next change.

If two writes use the same expected revision, one succeeds and the other gets a conflict with `current_revision` and `current_progress`. Think again about an out-of-date write before you send it again.

Every change needs an operation ID that you keep. A retry of a committed change with the same, unchanged request returns the original receipt. It does not repeat the effect or the history. A retry with a different name, scope or payload causes a conflict and returns no saved data. An Agent that was removed from a Task cannot change it or get receipts of its assignee operations. It can still read the ordinary Session metadata.

## Progress

A Task has one of four progress values: `waiting`, `working`, `finished` and `cancelled`. A new Task is Waiting, with the reason `Not started`.

- Waiting needs a reason. Working can have a reason.
- `--progress finished --summary TEXT` marks the whole Task as finished. It needs no approval from other assignees or the Orchestrator, and it does not check whether the prerequisites are finished.
- `--progress cancelled --reason TEXT` cancels the Task.
- A Finished or Cancelled Task needs `task reopen --reason TEXT` before its progress can change again. Reopening sets the Task to Waiting.

The last completion summary stays in the Task's `result` field, also after a later cancellation or reopen. The history keeps all earlier results, reasons and reports. The progress value is what counts: a retained result does not mean that the Task is finished now.

## Archived Tasks

An archived Task is a Finished or Cancelled Task that the browser view hides by default. Archiving changes no progress and deletes nothing. A Task counts as archived when one of three rules applies, and the first one that applies is its `archived_reason`:

1. `command`: the Orchestrator or the Operator ran `task archive`.
2. `chain`: the Task has at least one dependent, and every dependent is Finished or Cancelled. So the last Task of a chain stays visible, and so does finished work that a live Task depends on.
3. `age`: the Task became Finished or Cancelled 24 hours ago or more.

```sh
a-stack task archive --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --expected-revision 6 --operation-id archive-side-work --json
# The Operator runs the same command without --as and --credential-file.
a-stack task unarchive --workspace "$workspace_id" --session "$session_id" \
  --task "$task_id" --expected-revision 7 --operation-id unarchive-side-work --json
```

`task archive` works only on a Finished or Cancelled Task that the command has not archived yet. `task unarchive` undoes only the command. It cannot keep a Task visible against the chain or age rule. Both need an operation ID and the Task revision. They raise the revision and write a history entry, `task.archive` or `task.unarchive`. Assignees that are not the Orchestrator get `forbidden`, and so does the browser view. `task reopen` clears the archive and the end time, so a reopened Task is visible again.

a-stack stores only the command, as `archived_at`, and the time the Task last became Finished or Cancelled, as `ended_at`. It computes the chain and age rules in Go each time it reads a Task, so `task show`, `task list` and the browser view agree. Passing the age limit does not change the Session read revision. The snapshot ETag also counts the Tasks that the age rule archives, so an open browser view shows the change at its next poll, also in a quiet Session.

## Reports, edits and history

`task report --report TEXT` replaces the shared report. `task edit` accepts `--title`, `--description` and `--ticket`, in any combination. An explicitly empty description or Ticket reference clears it. If a large text does not change, a-stack reuses the stored copy.

`task history` pages through the retained changes and the changed text versions. It does not store a full copy of the Task for each change. Each entry has `actor_kind`: `agent` with the Agent's `actor_id`, or `operator` with no `actor_id`. Only `task archive` and `task unarchive` write Operator entries. An Operator change keeps the Task's `updated_by`, which always names an Agent. A new Task stores no text for an empty description or for its starting reason `Not started`. Reads still show both, and in the `task.create` history entry these two texts have no `id`.

## Hand a Task over

When an Agent stops before its Task is done, for example at a usage limit or after a crash, `task handoff --task T` prints a brief that a fresh Agent can start from:

- the Task, Session and Workspace IDs, the progress and revision, and the reason or result;
- the Workspace's [standing orders](lifecycle.md#standing-orders), in full, when there are some;
- the description and the report;
- the open Needs you question, who asked it and when, and the Operator's latest answer with its question;
- the unfinished prerequisites: every prerequisite that is not Finished, with its progress and reason;
- the assignees, as `@name` for mentions;
- the latest Task mail, oldest first: 20 messages, or `--limit N` (1 to 500).

The brief is plain text in Markdown. `--json` prints the same as `{"data":{"task":...,"asker_name":...,"unfinished_prerequisites":[...],"assignees":[...],"mail":{"readable":true,"total":37,"messages":[...]},"standing_orders":{...}}}`, where `standing_orders` is left out when there are none. The command only reads, with the same reads as `task show`, `task dependencies`, `task assignees`, `mail read` and `session inspect`, so it changes nothing. Only assignees read Task mail: for anyone else, such as the Operator or an Orchestrator that is not assigned, the brief gives the number of messages and says that the credential cannot read them. Text from Agents keeps its line breaks; other control characters are replaced. The command needs no new service: it works with a v0.1.6 service too, which has no standing orders.

## Export a Session

`session export --markdown` writes a Session summary for a PR or a hand-over. The Operator and every registered Agent of the Session can run it, also after the Session closes:

```sh
a-stack session export --workspace "$workspace_id" --session "$session_id" \
  --markdown --output session-summary.md
# An Agent can use --as agent --credential-file PRIVATE_FILE instead of the IDs.
```

The summary gives the Session name and full Workspace folder, then the Tasks in dependency order: prerequisites before dependents. Each Task has its title, progress, assignees, result summary, report and Task mail count. It includes open Needs you questions and the latest question answered by the Operator for each Task, with the answer. Standing orders are included in full when there are some.

Markdown is the default. `--output FILE` writes to a file instead of stdout, replacing an existing file only after the complete export is written. `--include-archived` includes archived Tasks; otherwise they are left out. `--json` writes the same data as `{"data":{"session_id":...,"name":...,"workspace_id":...,"workspace_path":...,"read_revision":...,"tasks":[...],"standing_orders":{...}}}`. Do not combine `--json` and `--markdown`.

The command only reads through existing endpoints. It never requests or exports Task mail bodies, even for an assignee. It collects paged Tasks, dependencies, assignments and full Task details at one Session read revision. If the Session changes during collection, it returns `conflict` without writing stdout or replacing the output file; run it again. It exports current reports and results, not their history. Review the reports, answers, orders and local folder path before sharing the file.

## Text input and limits

Instead of a text flag, you can give `--body TEXT` or `--body-file FILE`. They supply this field:

| Command | Field from `--body` or `--body-file` |
| --- | --- |
| `task create`, `task edit` | Description |
| `task report` | Report |
| `task status --progress finished` | Result summary |
| Other `task status`, `task reopen` | Reason |
| `task attention set`, `task attention resolve` | Question or note |
| `answer` | The answer |

`--body-file -` reads stdin. Give only one of the text flag, `--body` and `--body-file`. `task status` with `--progress waiting` or `cancelled` needs a reason, and with `--progress finished` a summary; without it, the error says which flag is missing, such as `--reason is required for --progress waiting`. A text error names its field, such as `title is required` or `reason exceeds 2048 bytes`.

| Text | Limit |
| --- | --- |
| Description, report | 64 KiB of UTF-8 |
| Reason, result, question, resolution note | 2,048 bytes |
| Answer | 4,096 bytes |
| Title | 256 bytes |
| Ticket reference | 1,024 bytes |

a-stack rejects text that is too long. It never cuts it. A nonempty Ticket reference must be unique in the Session. Ticket references are only metadata that you write. a-stack does not look them up in a tracker or sync them.

## Needs you questions

`task attention set --question TEXT` asks the Operator one explicit question. The Task then shows Needs you. A question is separate from the progress. A Task can have one open question at a time. A second request gets a conflict with `current_question` and the revision.

`task attention resolve --note TEXT` closes the question. It needs the current Task revision and a nonempty note. Viewing the Task, finishing or cancelling it, runtime observations and Session closure never clear the question. Waiting for a dependency never creates a Needs you question.

### Answer a question

The Operator can answer the open question from the CLI:

```sh
a-stack task show --workspace "$workspace" --session "$session" --task "$task"
a-stack answer --workspace "$workspace" --session "$session" --task "$task" \
  --question-id "$question" --text 'Google first, then GitHub.' --operation-id answer-login-1
```

`task show` prints the open question with its `id` under `question`. `--question-id` takes that ID. `--body TEXT` is the same as `--text`. For a longer answer, use `--body-file FILE`, or `--body-file -` for stdin. An answer is plain UTF-8 text of 4,096 bytes at most, with no control characters other than tabs and line breaks, as for [standing orders](lifecycle.md#standing-orders).

In one operation, `answer` saves the answer on the question, resolves the question, raises the Task's revision and sends the answer as Task mail from the Operator. The mail mentions every current assignee, so each one gets the usual [notice](notifications.md), and the mail nudge of the [hooks](hooks.md#task-mail-nudge) tells the Agent. An Agent that restarts finds the answer with `mail check`. The history entry is `task.answer` with `actor_kind` `operator`.

The question ID is the guard. A question never changes, and a new question gets a new ID. If that question was already answered, `answer` fails with `conflict` and names who answered and when, even if a newer question is now open. An Agent-resolved question still gives a conflict without an answerer. If the open question is not the one you read, the conflict includes `current_question`. A report or another change in the meantime does not stop the answer. A retry with the same operation ID returns the first receipt, and a new operation ID after a lost answer gets the `conflict`, so the Agents never get the answer twice. Only the Operator can answer: Agents, also the Orchestrator, get `forbidden`. The [browser view](browser.md#task-details) can send the same answer.

`task show` shows the latest answered question as `answered_question`, with `question`, `answer` and `resolved_at`. When answered through trusted Tailscale Serve, it also has `actor_login` and `actor_name`. The `answer` receipt includes those fields and `received_at`; a retry keeps the original actor. Task history and answer mail retain the same attribution. Without transport identity, the actor fields are absent and the label stays `Operator`. Its `resolver_id` is empty, because the Operator is not an Agent. A question that an Agent resolved keeps its `note` and `resolver_id`.

## File claims

Agents claim files or folders before editing shared areas. Claims are advisory metadata, not filesystem locks. Any current assignee can add or release a Task's claims in an open Session:

```sh
a-stack task claim --task "$task" --path src --path docs/api.md --expected-revision 3 --operation-id worker-claim-1
a-stack task claims --task "$task"
a-stack task unclaim --task "$task" --path src --expected-revision 4 --operation-id worker-unclaim-1
```

Give literal paths relative to the Workspace root, not the Agent's worktree. Both slash styles are accepted. Paths are cleaned, including `./` and trailing slashes; absolute paths, paths outside the root, controls and globs are rejected. `.` claims the whole Workspace. Matching is case-sensitive on Linux and macOS; Windows folds case. The service never opens these paths or resolves symlinks. Give at most 100 paths per command; a Task holds at most 100 claims. Adding an existing claim or releasing a missing one is safe, but still raises the Task revision.

A claim succeeds even if it overlaps another open Task. Its `warnings` list names that Task, its paths and its current Agents. Equal paths overlap, as do a folder and anything below it; `src` does not overlap `src-old`. Warnings include other open Sessions of the same Workspace, but never another Workspace or mail and reports from another Session. A closed Session's claims do not warn until it reopens. Agents should read the warnings and coordinate in Task mail before editing the same files.

`task claims` returns current `claims` and `warnings`. `task show` includes `claims` and `claim_warnings`; Task pages for the view include `claims`; `session inspect` includes its Tasks' claim metadata. Claims and overlaps are shown read-only in the view's Task details. Renaming a Task with claims refreshes overlap warnings in other Sessions of the Workspace. Finishing or cancelling a Task releases its claims in the same transaction. Reopening does not restore them. A retry returns the original receipt and the current overlap warnings, which may have changed since the first request.

## File references

`task reference add --location PATH_OR_URL [--reference-revision REV]` records a path or URL that people can see and copy. a-stack never reads or serves the file. `task references` lists the current references. `task reference remove --reference ID` removes a current reference. The row and its history stay.

A location can be 2,048 UTF-8 bytes at most, and a reference revision 128 bytes. a-stack rejects locations that start with an executable URL scheme (`javascript:`, `data:` or `vbscript:`). Programs that show references must show all text as plain text, and must treat a reference as text to copy. They must not turn it into markup or a link.

## Dependencies

`dependency add --from A --to B` means that B depends on A. `dependency remove` takes the same two Tasks. Both commands need the Orchestrator and an operation ID. Their `--expected-revision` is the Session's `graph_revision` from `session inspect`. It is not the lifecycle revision or a Task revision.

In the same transaction, a-stack rejects a duplicate dependency, a dependency from a Task to itself, a dependency between two Sessions and a dependency that makes a cycle. Two opposite dependencies that are each valid alone cannot both be committed.

`dependency history` lists the compact history of dependency changes. `task dependencies --task ID` lists the prerequisites of a Task and their current progress. Task details also show `cancelled_prerequisite_count`.

Creating a Task or changing a dependency increases `graph_revision`, by one for a create with any number of `--after` prerequisites. Other Task changes increase the Task revision and the Session `read_revision`, but not `graph_revision`. When a prerequisite is finished or cancelled, its dependent Tasks do not change, and they can still be finished.

## Closed Sessions

A closed Session keeps all statuses and history. It rejects Task, graph and question changes until the Operator reopens it. Authorized reads and the original receipts stay available. No Task command starts or stops Agent processes or calls Herdr.

## Read API

All read endpoints below use authenticated `POST` JSON requests and return a `data` object. They use the same `Host`, `Origin` and `X-A-Stack-Instance` checks as the rest of the [HTTP API](foundation.md#http-rules), and they reject unknown fields. The Operator, the registered Agents of the Session and the Session's browser credentials can use them. Browser credentials cannot change anything, and cannot use the lifecycle, private or storage endpoints. These reads never return mail bodies, credential hashes, tokens, runtime IDs, raw bindings or Herdr aliases.

1. `POST /v1/session/snapshot` with `{workspace_id, session_id, known_revision}` returns a `SessionSnapshot`. `known_revision` is optional. The snapshot has `source: "local-service"`, `captured_at`, the Session with its lifecycle, read and graph revisions, `workspace_name` (the last part of the Workspace path, such as `my-project`; the full path is not included), `unchanged`, and the counts of Tasks, Agents, dependencies and assignments. If `known_revision` matches, the response has `unchanged: true` and no counts. Keep your saved data in that case. Missing counts do not mean an empty graph.
2. `POST /v1/session/page` with the context and `{kind, read_revision, after, limit, task_id}` returns `{read_revision, items, next}`. All fields after `kind` are optional, and `next` is present only when more items follow. The kinds are `tasks`, `agents`, `assignments`, `dependencies`, `references`, `history` and `graph_history`. `references` and `history` need a Task. `assignments` and `dependencies` can filter by Task. For dependencies, the filter returns the Task's prerequisites. The other kinds reject a Task filter.
3. `POST /v1/task/show` with the context and `{task_id, read_revision}` returns a `TaskDetail`: the Task summary, the description, report, reason and result, the open question and the read revision. `read_revision` is optional. Assignees, references and dependencies are separate pages, so one Task can never make a response grow without limit.

The endpoints `/v1/task/{list,history,assignees,references,dependencies}` are short forms of `session page` for one kind. All of them except `list` need a Task ID. In the CLI, use `session snapshot`, `session page --page-kind KIND` and the Task read commands.

A page returns 50 records by default and 100 at most. A page also stops at 768 KiB of encoded items and then gives a `next` cursor. a-stack never drops a retained Task to make a response fit.

### What a page contains

A Task item has:

- The ID, the title and the Ticket reference.
- The progress and the revision.
- The Unassigned flag and the number of assignees.
- The Needs you flag and `question_preview`, the first 160 Unicode code points of the open question.
- The numbers of references, prerequisites and cancelled prerequisites.
- The actor IDs, and the creation, update and report times.
- `report_preview`, the first 160 Unicode code points of the report.
- `last_action`, the action of the latest history entry, such as `task.status`. It happened at `updated_at`.
- `archived`, `archived_reason` (`command`, `chain` or `age`), `archived_at` and `ended_at`, as described in [Archived Tasks](#archived-tasks).
- `mail_count`, the number of messages in the Task's mail. It is the last mail sequence, read from the `(task_id, sequence)` index.
- `last_mail_at`, the time of the latest Task mail, left out when the Task has none.
- `quiet`, true for a Working Task with no change and no Task mail for 30 minutes that does not wait for an answer to a Needs you question. The read derives it from `updated_at` and `last_mail_at`; nothing is stored. See [Quiet Tasks](watch.md#quiet-tasks).

It has no full report, question or description body.

An assignment item has the Task and Agent IDs, the Agent name and the time of the assignment. Agent pages give the team metadata without private data. An Agent's runtime observation has its own `observed_at`, a `runtime_source` (`not-observed` or `caller-observation`) and `runtime_verified: false`. The age of a Task comes from `updated_at` and `report_at`. The age of a runtime observation comes from `observed_at`. Both are relative to the snapshot's `captured_at` plus the time that has passed since. a-stack never derives a runtime observation from Task progress.

### Read a consistent graph

To read one consistent graph, get the snapshot first. Then load all pages of Tasks, Agents, assignments and dependencies with its `session.read_revision`. A cursor is bound to the Session, the kind, the optional Task, the revision and a keyset position. If a visible change happens in between, the next page returns `conflict` with the new read revision. Then start the collection again, and keep the last complete view until the new one is complete. Never mix pages from different revisions. Never replace saved data with an empty graph because of a connection error. Each read uses its own short transaction. No transaction stays open while a client waits or polls.

Load the details, history and references of a selected Task only when you need them, with the same revision. A history response includes the changed texts, questions and references of each entry. Large old reports are paged, and are not copied into every event or into one response. Use `graph_revision` to tell changes to the graph apart from updates of reports, questions or status. On an ordinary update, keep the positions, the selection and the view. Items come in a stable ID order.

### Measured sizes

The client accepts 1 MiB at most for each response. The tests use the worst case of an escaped 64 KiB description and report plus notes and a question: an 831,851-byte detail response. Retained history in the tests needs four pages, and the largest page is 432,112 bytes.

Fixtures with 10, 100 and 1,000 Tasks list every Task in 1, 1 and 10 pages. For 1,000 Tasks, the Task collection is about 556 KiB in total, with pages of about 56 KiB. With the service's statement cache, the long Task query is prepared once. In one local run, the 1,000 Tasks took about 22 ms. A chain of 1,000 Finished Tasks, where the chain rule archives 999, took about 27 ms. These are measurements of backend fixtures. They make no promise about the browser view or about production throughput. a-stack sets no limit on the total number of retained Tasks or history entries, and it never deletes them.

## Internals

Schema v6 adds `tasks.archived_at` and `tasks.ended_at`, and fills `ended_at` of done Tasks from their last status entry in the history. It also rebuilds `task_history` with `actor_kind`, so that an Operator entry has no `actor_id`, while Agent entries keep their foreign key to `agents`.

Schema v3 adds `tasks`, immutable `task_content`, current `task_assignees`, retained `task_questions` and `task_references`, delta `task_history`, active `dependencies` and retained `graph_history`. It also adds `sessions.graph_revision`. Actor and content IDs stay stable after a rebind, a role change or a lost runtime. Foreign keys on the assignee table make sure that the Task and the Agent are in the same Session. An existing v1 or v2 database is upgraded in one transaction. Unsupported future schemas are still rejected.

Mail code uses `ReadAssignedTask(ctx, tx, principal, Context, taskID)` to check the current registration and membership, also for authorized reads in a closed Session. A browser credential's mail read checks only that the Task belongs to its Session. A send also calls `OpenSession` in the same `Store.Mutate` apply transaction. Mail is append-only, so a send does not need a Task revision. A replay also checks current membership. The index `task_assignees_agent(agent_id, task_id)` lets a caller find its Tasks. It is not a record of unread mail. The Task and Session read endpoints never return mail bodies.

The read data gives no authority for automatic notices. Runtime bindings stay private and unverified in these reads. Herdr notices are a separate part, described in [Mention notices](notifications.md).
