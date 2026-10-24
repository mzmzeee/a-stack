# Shared Tasks and graph

Tasks belong to one explicitly selected Workspace/Session. All commands take `--workspace ID --session ID`; Agent commands also select `--as agent --credential-file PRIVATE_FILE`. Use the lifecycle guide to enroll an Operator, Orchestrator and workers first. Changing cwd never changes context.

The current Orchestrator creates Tasks, edits title/description/ticket metadata, changes assignment and adds/removes dependencies. Current assignees own status, reports, file references and Operator attention. The Orchestrator must assign itself for those ordinary assignee actions; the Operator role never acts as a worker. Replacing the role removes graph authority immediately, while existing assignee authority remains. Runtime unknown/idle/done does not change either authority or Task progress.

## CLI workflow

Examples assume the IDs and private credentials from `docs/lifecycle.md`; add the same `--state-dir` everywhere when using temporary/custom state.

```sh
./a-stack task create --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --title "Build feature" \
  --body-file work-description.txt --ticket '#42' --operation-id task-create-42 --json
# Set task_id to data.resource_id; a new Task has revision 1.
./a-stack task assign --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --agent "$worker_a_id" --expected-revision 1 --operation-id assign-worker-a --json
./a-stack task assign --as agent --credential-file "$orchestrator_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --agent "$worker_b_id" --expected-revision 2 --operation-id assign-worker-b --json
./a-stack task status --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --progress working --expected-revision 3 --operation-id task-start-work --json
```

Both assignees see one ID, revision and status. `task show` reads current detail; `task list` includes Unassigned Tasks. Use the returned revision for the next change. Concurrent writes with one expected revision yield one success and one conflict carrying `current_revision` and `current_progress`. Reconsider a stale write before retrying. Mutations require retained operation IDs; an unchanged committed retry returns its original receipt without repeating effects/history. Changed name/scope/payload conflicts without saved resource data. Removed assignees cannot mutate or recover assignee-operation receipts, but retain their ordinary Session metadata reads.

Progress values are `waiting`, `working`, `finished`, `cancelled`. New Tasks are Waiting with `Not started`. Waiting requires a reason; Working may have a reason. Whole-Task completion uses `--progress finished --summary TEXT`, needs no peer/Orchestrator approval and does not inspect prerequisite completion. Cancellation requires `--progress cancelled --reason TEXT`. Finished/Cancelled Tasks require `task reopen --reason TEXT` before another status transition; reopen returns Waiting. The last successful completion summary stays in detail's `result` through later cancellation/reopen, with all earlier results/reasons/reports retained in history. Progress is authoritative; a retained result does not imply current completion.

`task report --report TEXT` updates the shared report. `task edit` accepts any of `--title`, `--description`, `--ticket`; an explicitly empty description/ticket clears it. Unchanged large text is reused. `task history` paginates retained changes and changed text versions; it stores no repeated full Task snapshot.

| Command | Text alternative to flags |
| --- | --- |
| `task create`, `task edit` | `--body-file FILE` supplies description |
| `task report` | supplies report |
| `task status --progress finished` | supplies result summary |
| other `task status`, `task reopen` | supplies reason |
| `task attention set`, `task attention resolve` | supplies question or note |

`--body-file -` reads stdin. Explicit text and a body file for the same field conflict. Description/report limits are 64 KiB UTF-8, reasons/results/questions/resolution notes 2,048 bytes, titles 256 bytes, ticket references 1,024 bytes. Input is rejected rather than truncated. Exact nonempty ticket references are unique within one Session; references are manual metadata, with no tracker lookup or synchronization.

## Attention and file references

`task attention set --question TEXT` opens one explicit Operator question, independently of progress. A second request returns a conflict with `current_question` and revision. `task attention resolve --note TEXT` requires the current Task revision and a nonempty resolution note. Viewing, finishing/cancelling, runtime observations and closure never clear the question. Dependency waits alone never create Needs you.

`task reference add --location PATH_OR_URL [--reference-revision REV]` records display/copy metadata only; it never reads or serves the referenced file. `task references` lists current references, and `task reference remove --reference ID` removes the current reference while preserving its row/history. Locations are at most 2,048 UTF-8 bytes, optional revisions 128; executable URL prefixes are rejected. Consumers must render all supplied text as text and keep references as copy metadata, not executable markup/navigation.

## Dependencies and lifecycle

`dependency add --from A --to B` means **B depends on A**. `dependency remove` takes the same endpoints. Both require the Orchestrator, an operation ID and `--expected-revision` containing the **Session graph revision** from `session inspect`, rather than the lifecycle or Task revision. Reject duplicate/self/cross-Session/cyclic links inside the same transaction. Two individually plausible opposing edge writes cannot both commit. `dependency history` retains compact edge events; `task dependencies --task ID` lists incoming prerequisites and their current progress. Detail also reports `cancelled_prerequisite_count`.

Task creation and edge edits advance `graph_revision`. Other Task changes advance the Task revision and Session `read_revision`, leaving topology revision unchanged. Completing/cancelling a prerequisite never changes its dependents or vetoes their completion. Closure preserves all statuses and history, rejecting Task/graph/attention writes until explicit Operator reopen. Authorized reads and original receipts remain available after closure. No command starts/stops agent processes or calls Herdr.

## Bounded Session read contract

All endpoints below use authenticated **POST JSON**, the foundation's exact Host/Origin and `X-A-Stack-Instance` checks, and a `data` envelope. Unknown fields fail. Operator, registered Session agents and Session-scoped browser capabilities may read this model. Browser capabilities cannot mutate or use lifecycle/private/storage endpoints; these reads expose no mail, credential hashes/tokens, runtime IDs, raw bindings or Herdr aliases.

1. `POST /v1/session/snapshot` with `{workspace_id, session_id, known_revision?}` returns `SessionSnapshot`: `source: "local-service"`, `captured_at`, the Session including lifecycle/read/graph revisions, `unchanged`, and counts for Tasks/agents/dependencies/assignments. A matching known revision returns `unchanged: true` and omits counts. Retain saved data; omitted counts do not mean an empty graph.
2. `POST /v1/session/page` with context plus `{kind, read_revision?, after?, limit?, task_id?}` returns `{read_revision, items, next?}`. Collections are `tasks`, `agents`, `assignments`, `dependencies`, `references`, `history`, `graph_history`. `references`/`history` require a Task; assignment/dependency pages may filter by Task (dependencies filter incoming edges). Other collections reject a Task filter.
3. `POST /v1/task/show` with context and `{task_id, read_revision?}` returns `TaskDetail`: current Task summary, description/report/reason/result, current open question and read revision. Assignees/references/dependencies are separate pages, so one Task cannot produce unbounded nested arrays.

Read aliases are `/v1/task/{list,history,assignees,references,dependencies}`; they select the corresponding collection. Task detail aliases require a Task ID. CLI equivalents include `session snapshot`, `session page --page-kind KIND`, and the Task read commands above. Lists default to 50, maximum 100 records, and also enforce a **768 KiB encoded-item budget** with continuation. No retained Task is silently dropped to fit one response.

Task overview items include ID/title/ticket, progress/revision, Unassigned/assignee count, Needs you, reference/prerequisite/cancelled-prerequisite counts, actor IDs and creation/update/report times. They omit report/description bodies. Assignment items pair Task and Agent IDs with names and assignment receipt time; agent pages provide the sanitized team metadata. Agent runtime observations have their own `observed_at`, `runtime_source` (`not-observed` or `caller-observation`) and `runtime_verified: false` until a later verified integration. Task age uses `updated_at`/`report_at`; runtime age uses `observed_at`, relative to snapshot `captured_at` plus elapsed display time. No runtime observation is inferred from Task progress.

For one coherent graph, capture the manifest, then load all Task/agent/assignment/dependency pages pinned to its `session.read_revision`. Cursors are bound to Session, collection, optional Task, revision and keyset position. Any intervening visible mutation returns `conflict` with the new read revision; restart collection and retain the last complete view. Never merge inconsistent pages or replace saved data with an empty graph on a connection error. Each read holds only its own short transaction; no transaction spans polling/network waits.

Fetch selected Task detail/history/references on demand with that revision. History responses hydrate changed text/question/reference records; large old reports paginate instead of copying into every event or a single response. Use `graph_revision` to distinguish topology changes from report/attention/status updates and preserve positions/selection/viewport on ordinary updates. Stable ID ordering is available; browser layout/ranks remain the browser ticket's work.

The foundation client accepts at most **1 MiB per response**. Tests exercise worst escaped 64 KiB description/report plus notes/question (831,851-byte detail) and retained history split across four pages (largest 432,112 bytes). Actual 10/100/1,000-Task fixtures enumerate every Task in 1/1/10 pages; the 1,000-Task fixture's overview collection is about 509 KiB total, with pages about 51 KiB. These are backend fixture measurements, not a UI or production throughput promise. No total retained Task/history limit or purge is introduced.

## Handoff to mail and browser slices

Schema v3 adds `tasks`, immutable `task_content`, current `task_assignees`, retained `task_questions`/`task_references`, delta `task_history`, active `dependencies` and retained `graph_history`; it adds `sessions.graph_revision`. Actor/content IDs are stable across rebind/role/runtime loss. Assignee junction foreign keys enforce same-Session endpoints. Existing populated v1/v2 data upgrades transactionally; unsupported future schemas still reject.

Mail should use `ReadAssignedTask(ctx, tx, principal, Context, taskID)` for current registration/membership, including closed authorized reads. Sends additionally call `OpenSession` in the **same** `Store.Mutate.apply` transaction; append-only mail need not require a Task revision. Replay must check current membership too. `task_assignees_agent(agent_id, task_id)` supports caller Task lookup; it is not an unread ledger. Existing Task/Session read endpoints must remain free of mail bodies.

Browser code can use the manifest/pages/detail contract without changing Task mutations or reading SQLite directly. It must add its scoped view bootstrap/conditional polling/assets, verify rendering in both themes, and preserve the approved prototype. Current read data contains no automatic-delivery authority: bindings remain private/unverified and Herdr integration belongs to its own slice.
