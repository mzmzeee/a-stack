# a-stack command reference for Agents

The examples use the `me` and `rev` helpers from [SKILL.md](../SKILL.md). `T`, `A` and `B` are Task IDs. `a-stack --help --json` lists all commands and flags.

Each command prints `{"data": ...}` on stdout. A change returns a receipt: `data.resource_id` is the ID of the new Agent, Task, message or dependency, or of the changed Task, and `data.revision` is its new revision.

## Read the Session

| Command | What you get |
| --- | --- |
| `me session inspect` | `data.revision`, `data.graph_revision`, `data.orchestrator_id`, `data.closed` |
| `me session snapshot --known-revision 12` | Counts of Tasks, Agents, dependencies and assignments, or `unchanged: true` if `read_revision` is still 12 |
| `me agent list` | `data.items[]` with each Agent's `id` and `name`; find your own ID here |
| `me task list` | `data.items[]` with `id`, `title`, `progress`, `revision`, `unassigned`, `needs_you` |
| `me task show --task T` | One Task with `revision`, `progress`, `description`, `report`, `reason`, `result` |
| `me task assignees --task T` | `data.items[].agent_name` and `agent_id` |
| `me task history --task T` | Every change of the Task, with the changed texts |
| `me task references --task T` | The current file references, each with its `id` |
| `me task dependencies --task T` | The prerequisites of T and their progress |
| `me dependency history` | The dependency changes of the Session |

Lists return `data.items` and, when more follow, `data.next`. Get the next page with `--after "$next"`. `--limit` takes 1 to 100.

## Change a Task (assignee)

Every change needs `--expected-revision "$(rev T)"` and a new `--operation-id`.

```sh
me task status --task T --progress working --expected-revision "$(rev T)" --operation-id 02-worker-start-1
me task status --task T --progress waiting --reason 'Waits for the API Task' --expected-revision "$(rev T)" --operation-id 02-worker-wait-1
me task status --task T --progress finished --summary 'API and tests done' --expected-revision "$(rev T)" --operation-id 02-worker-finish-1
me task status --task T --progress cancelled --reason 'Replaced by another Task' --expected-revision "$(rev T)" --operation-id 02-worker-cancel-1
me task reopen --task T --reason 'Review found a bug' --expected-revision "$(rev T)" --operation-id 02-worker-reopen-1
me task report --task T --report 'Done: API, tests. Open: docs.' --expected-revision "$(rev T)" --operation-id 02-worker-report-1
me task attention set --task T --question 'Ship in 1.2 or 1.3?' --expected-revision "$(rev T)" --operation-id 02-worker-ask-1
me task attention resolve --task T --note 'Operator chose 1.3' --expected-revision "$(rev T)" --operation-id 02-worker-answer-1
me task reference add --task T --location docs/api.md --expected-revision "$(rev T)" --operation-id 02-worker-ref-1
me task reference remove --task T --reference REFERENCE_ID --expected-revision "$(rev T)" --operation-id 02-worker-unref-1
```

- Waiting needs `--reason`. Finished needs `--summary`. Cancelled needs `--reason`.
- A Finished or Cancelled Task needs `task reopen` before its progress changes again. Reopening sets it to Waiting.
- Finishing does not ask the other assignees or check the prerequisites. It marks the whole shared Task as done.
- A Task has at most one open question. Only `task attention resolve` closes it.
- A file reference is a path or URL that people can copy. a-stack never reads the file. `task reference remove` takes the reference `id` from `task references`.

### Long text

Instead of the text flag, give `--body-file FILE`, or `--body-file -` for stdin. Never give both.

| Command | `--body-file` sets |
| --- | --- |
| `task report` | The report (64 KiB at most) |
| `task status --progress finished` | The summary (2,048 bytes at most) |
| Other `task status`, `task reopen` | The reason (2,048 bytes at most) |
| `task attention set` / `resolve` | The question / the note (2,048 bytes at most) |
| `task create`, `task edit` | The description (64 KiB at most) |
| `mail send` | The body (64 KiB at most) |

a-stack rejects text that is too long. It never cuts it.

## Task mail

Only the current assignees of a Task can send or read its mail. Mail needs no revision.

```sh
me mail send --task T --body 'Ready for review, @03-reviewer.' --operation-id 02-worker-mail-02
me mail send --task T --body-file /your/private/folder/notes.md --operation-id 02-worker-mail-03
me mail read --task T --limit 100
me mail check
```

- `@name` in the body, and each `--mention NAME` (without `@`, repeatable), must be a current assignee of T. Otherwise the whole send fails. Check with `me task assignees --task T`.
- The receipt of a send has the message ID in `resource_id` and its sequence number in `revision`.
- `mail read` returns `data.messages[]` (`id`, `sequence`, `sender_id`, `received_at`, `body`, `mention_ids`), `data.cursor` and `data.more`. Give the cursor back with `--cursor "$cursor"` to read only newer messages.
- `mail check` reads all your Tasks at once: `data.tasks[]`, each with `task_id`, `messages`, `cursor` and `more`. Without a cursor file, it starts at the first message of every Task each time.
- `sender_id` is an Agent ID. Map it to a name with `me task assignees --task T`. Mail reads include your own messages; find your own ID in `me agent list`.

### Read only new mail with a cursor file

Keep the cursor file in your private folder. The CLI reads it but never writes it, so merge the returned cursors yourself:

```sh
F=/your/private/folder/cursors.json
[ -f "$F" ] || echo '{}' >"$F"
me mail check --cursor-file "$F" >/your/private/folder/check.json
python3 -c 'import json,sys
f,r=sys.argv[1],json.load(open(sys.argv[2]))["data"]
c=json.load(open(f))
for t in r["tasks"]:
    c[t["task_id"]]=t["cursor"]
    for m in t["messages"]: print(t["task_id"], m["sequence"], m["sender_id"], m["body"], sep="\t")
json.dump(c,open(f,"w"))
print("next_task", r.get("next_task",""))' "$F" /your/private/folder/check.json
```

If `next_task` is not empty, run the check again with `--after-task "$next_task"`. If a Task has `more: true`, run the check again.

## Errors

| `error.code` | Exit | Meaning and what to do |
| --- | --- | --- |
| `invalid_input` | 2 | A flag, ID or text is wrong. A too short operation ID also gives this, with a generic message. Fix the command; use a new operation ID. |
| `conflict` | 1 | The revision changed (`current_revision`, `current_progress`), or a question is already open (`current_question`). Read again, rethink, retry with a new operation ID. |
| `operation_conflict` | 1 | This operation ID was already used for a different change. Use a new ID. |
| `forbidden` | 1 | You are not allowed, for example you are not an assignee of the Task or not the Orchestrator. |
| `not_found` | 1 | The ID does not exist in your Session. |
| `session_closed` | 1 | The Operator closed the Session. Reads still work. Wait or ask the Operator. |
| `unauthorized` | 1 | The credential file is wrong or was replaced. Ask the Operator. |
| `storage_unavailable` | 1 | The service is not reachable or failed. Retry the same command with the same operation ID later. Never start the service yourself. |

If a response gets lost, send the same command again with the same operation ID and the same flags. You get the original receipt, and nothing changes twice.

## Orchestrator commands

Only the current Orchestrator can run these. See [orchestrator.md](orchestrator.md).

```sh
me agent register --name 02-worker --new-credential-file /private/creds/02-worker.json --operation-id orch-register-02-worker
me task create --title 'Build the API' --body-file /your/private/folder/api.md --ticket '#42' --operation-id orch-task-api-1
me task edit --task T --title 'Build the REST API' --expected-revision "$(rev T)" --operation-id orch-edit-api-1
me task assign --task T --agent AGENT_ID --expected-revision "$(rev T)" --operation-id orch-assign-api-02
me task unassign --task T --agent AGENT_ID --expected-revision "$(rev T)" --operation-id orch-unassign-api-02
me dependency add --from A --to B --expected-revision "$(grev)" --operation-id orch-dep-a-b-1
me dependency remove --from A --to B --expected-revision "$(grev)" --operation-id orch-undep-a-b-1
```

`grev` prints the Session's `graph_revision`. [orchestrator.md](orchestrator.md) shows the script. `--from A --to B` means that B depends on A.
