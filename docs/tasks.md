# Tasks and the Task graph

A Task belongs to one Session. The Operator names the Session in each Task command with `--workspace ID --session ID`. An Agent runs Task commands with `--as agent --credential-file PRIVATE_FILE`. A credential file from v0.1.5 or later names the Session, so the Agent can leave both IDs out (see [Credential context](lifecycle.md#credential-context)). The current directory never changes the context. (Only the Operator commands `a-stack`, `a-stack -r` and `a-stack new` use it, to choose a Session to open. See [Workspaces, Sessions and Agents](lifecycle.md).) Before you use these commands, enroll the Operator, the Orchestrator and the other Agents as described in [Workspaces, Sessions and Agents](lifecycle.md).

## Who can do what

The current Orchestrator defines the work. It creates Tasks, edits their title, description and Ticket reference, assigns and unassigns Agents, and adds and removes dependencies.

The current assignees do the work. An assignee is an Agent that is assigned to the Task. Assignees update the progress, the report, the file references and the Operator questions.

The Orchestrator must assign itself to a Task before it can do assignee actions on it. The Operator never acts as an assignee. When the Operator replaces the Orchestrator, the old Agent loses its graph authority immediately, but it keeps its assignee authority. A runtime state of unknown, idle or done changes no authority and no Task progress.

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

## Text input and limits

Instead of a text flag, you can give `--body TEXT` or `--body-file FILE`. They supply this field:

| Command | Field from `--body` or `--body-file` |
| --- | --- |
| `task create`, `task edit` | Description |
| `task report` | Report |
| `task status --progress finished` | Result summary |
| Other `task status`, `task reopen` | Reason |
| `task attention set`, `task attention resolve` | Question or note |

`--body-file -` reads stdin. Give only one of the text flag, `--body` and `--body-file`. `task status` with `--progress waiting` or `cancelled` needs a reason, and with `--progress finished` a summary; without it, the error says which flag is missing, such as `--reason is required for --progress waiting`. A text error names its field, such as `title is required` or `reason exceeds 2048 bytes`.

| Text | Limit |
| --- | --- |
| Description, report | 64 KiB of UTF-8 |
| Reason, result, question, resolution note | 2,048 bytes |
| Title | 256 bytes |
| Ticket reference | 1,024 bytes |

a-stack rejects text that is too long. It never cuts it. A nonempty Ticket reference must be unique in the Session. Ticket references are only metadata that you write. a-stack does not look them up in a tracker or sync them.

## Needs you questions

`task attention set --question TEXT` asks the Operator one explicit question. The Task then shows Needs you. A question is separate from the progress. A Task can have one open question at a time. A second request gets a conflict with `current_question` and the revision.

`task attention resolve --note TEXT` closes the question. It needs the current Task revision and a nonempty note. Viewing the Task, finishing or cancelling it, runtime observations and Session closure never clear the question. Waiting for a dependency never creates a Needs you question.

## File references

`task reference add --location PATH_OR_URL [--reference-revision REV]` records a path or URL that people can see and copy. a-stack never reads or serves the file. `task references` lists the current references. `task reference remove --reference ID` removes a current reference. The row and its history stay.

A location can be 2,048 UTF-8 bytes at most, and a reference revision 128 bytes. a-stack rejects locations that start with an executable URL scheme (`javascript:`, `data:` or `vbscript:`). Programs that show references must show all text as plain text, and must treat a reference as text to copy. They must not turn it into markup or a link.

## Dependencies

`dependency add --from A --to B` means that B depends on A. `dependency remove` takes the same two Tasks. Both commands need the Orchestrator and an operation ID. Their `--expected-revision` is the Session's `graph_revision` from `session inspect`. It is not the lifecycle revision or a Task revision.

In the same transaction, a-stack rejects a duplicate dependency, a dependency from a Task to itself, a dependency between two Sessions and a dependency that makes a cycle. Two opposite dependencies that are each valid alone cannot both be committed.

`dependency history` lists the compact history of dependency changes. `task dependencies --task ID` lists the prerequisites of a Task and their current progress. Task details also show `cancelled_prerequisite_count`.

Creating a Task or changing a dependency increases `graph_revision`. Other Task changes increase the Task revision and the Session `read_revision`, but not `graph_revision`. When a prerequisite is finished or cancelled, its dependent Tasks do not change, and they can still be finished.

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
