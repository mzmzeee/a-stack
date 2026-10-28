# a-stack command reference for Agents

The examples use the `me` helper from [SKILL.md](../SKILL.md). `T`, `A` and `B` are Task IDs, and `R` is a Task revision. `a-stack --help --json` lists all commands and flags.

Each command prints `{"data": ...}` on stdout. A change returns a receipt: `data.resource_id` is the ID of the new Agent, Task, message or dependency, or of the changed Task, and `data.revision` is its new revision.

## Enroll your own hook session

After installing hooks, run `me hook enroll --claude` (or `--codex`, `--pi`, `--opencode`) from your own session in the hook working folder. It detects native session metadata; it does not install config files. With no flag it requires one unambiguous harness. If metadata is missing, pass `--native-session NATIVE_ID` or use your own `me herdr bind`. This local command needs no operation ID or expected revision. Success prints `enrolled: true` with `--json`. Every new native session must enroll; compaction/resume of the same ID needs no repeat.

## Read the Session

| Command | What you get |
| --- | --- |
| `me session inspect` | `data.revision`, `data.graph_revision`, `data.orchestrator_id`, `data.closed`, your own Agent ID in `data.your_agent_id`, and the Operator's standing orders in `data.standing_orders.body` when there are some |
| `me workspace orders show` | The standing orders of your Workspace as `data.body` (empty when there are none) |
| `me session snapshot --known-revision 12` | Counts of Tasks, Agents, dependencies and assignments, or `unchanged: true` if `read_revision` is still 12 |
| `me agent list` | `data.items[]` with each Agent's `id` and `name`; find your own ID here |
| `me task list` | `data.items[]` with `id`, `title`, `progress`, `revision`, `unassigned`, `needs_you`, `prerequisite_count`, `quiet` (Working, but no change and no Task mail for 30 minutes) |
| `me task show --task T` | One Task with `revision`, `progress`, `description`, `report`, `reason`, `result` |
| `me task assignees --task T` | `data.items[].agent_name` and `agent_id` |
| `me task history --task T` | Every change of the Task, with the changed texts |
| `me task references --task T` | The current file references, each with its `id` |
| `me task dependencies --task T` | The prerequisites of T and their progress |
| `me task handoff --task T` | A plain-text brief to take over T: its description, report, open question, unfinished prerequisites, assignees and the last 20 Task mails (`--limit N` for more). Only assignees see the mail. `--json` gives the same as data |
| `me dependency history` | The dependency changes of the Session |

Lists return `data.items` and, when more follow, `data.next`. Get the next page with `--after "$next"`. `--limit` takes 1 to 100.

## Change a Task (assignee)

Every change needs `--expected-revision R` and a new `--operation-id`. `R` is the `revision` from your last `task show` of T, or from the receipt of your last change to T. Read the Task again only after a `conflict`.

```sh
me task status --task T --progress working --expected-revision R --operation-id 02-worker-start-1
me task status --task T --progress waiting --reason 'Waits for the API Task' --expected-revision R --operation-id 02-worker-wait-1
me task status --task T --progress finished --summary 'API and tests done' --expected-revision R --operation-id 02-worker-finish-1
me task status --task T --progress cancelled --reason 'Replaced by another Task' --expected-revision R --operation-id 02-worker-cancel-1
me task reopen --task T --reason 'Review found a bug' --expected-revision R --operation-id 02-worker-reopen-1
me task report --task T --report 'Done: API, tests. Open: docs.' --expected-revision R --operation-id 02-worker-report-1
me task attention set --task T --question 'Ship in 1.2 or 1.3?' --expected-revision R --operation-id 02-worker-ask-1
me task attention resolve --task T --note 'Operator chose 1.3' --expected-revision R --operation-id 02-worker-answer-1
me task reference add --task T --location docs/api.md --expected-revision R --operation-id 02-worker-ref-1
me task reference remove --task T --reference REFERENCE_ID --expected-revision R --operation-id 02-worker-unref-1
```

- Waiting needs `--reason`. Finished needs `--summary`. Cancelled needs `--reason`.
- A Finished or Cancelled Task needs `task reopen` before its progress changes again. Reopening sets it to Waiting.
- Finishing does not ask the other assignees or check the prerequisites. It marks the whole shared Task as done.
- A Task has at most one open question. `task attention resolve` closes it, and so does the Operator's answer from `a-stack answer` or the browser view. That answer arrives as Task mail from the Operator and raises the Task's revision. `task show` then shows it in `answered_question` (`question`, `answer`, `resolved_at`).
- A file reference is a path or URL that people can copy. a-stack never reads the file. `task reference remove` takes the reference `id` from `task references`.

### Long text

Instead of the text flag, give `--body-file FILE`, or `--body-file -` for stdin. `--body TEXT` also works on every command below. Give only one of them.

| Command | `--body` or `--body-file` sets |
| --- | --- |
| `task report` | The report (64 KiB at most) |
| `task status --progress finished` | The summary (2,048 bytes at most) |
| Other `task status`, `task reopen` | The reason (2,048 bytes at most) |
| `task attention set` / `resolve` | The question / the note (2,048 bytes at most) |
| `task create`, `task edit` | The description (64 KiB at most) |
| `mail send` | The body (64 KiB at most) |

a-stack rejects text that is too long. It never cuts it.

## File claims

Before editing shared files, run `me task claim --task T --path src --path docs/api.md --expected-revision R --operation-id 02-worker-claim-1`. Paths are literal files or folders relative to the Workspace root, not globs. Use `.` for the whole Workspace. Give at most 100 paths; a Task holds at most 100 claims. Read `data.warnings`: each overlap names the other Task, its paths and its current Agents. Coordinate in Task mail; claims warn but never block another Task. The receipt raises the Task revision, so use it as your next `R`.

`me task claims --task T` returns current `claims` and `warnings`. `task show` carries `claims` and `claim_warnings`; `session inspect` carries its Tasks' claim metadata. `me task unclaim --task T --path src --expected-revision R --operation-id 02-worker-unclaim-1` releases paths. Any current assignee may change the Task's claims. Finishing or cancelling releases them all; reopening does not restore them. Claims in closed Sessions do not warn until the Session reopens. Warnings may name Tasks and Agents in other Sessions of the same Workspace, but carry no reports or mail.

## Task mail

Only the current assignees of a Task can send or read its mail. Mail needs no revision.

```sh
me mail send --task T --body 'Ready for review, @03-reviewer.' --operation-id 02-worker-mail-02
me mail send --task T --body-file /your/private/folder/notes.md --operation-id 02-worker-mail-03
me mail reply --message MESSAGE_ID --body 'Confirmed.' --operation-id 02-worker-reply-01
me mail recipients --task T
me mail read --task T --limit 100
me mail check --cursor-file /your/private/folder/mail.json
```

- `@name` in the body, and each `--mention NAME` (without `@`, repeatable), must be a current assignee of T. Otherwise the whole send fails. Check with `me mail recipients --task T`.
- `mail reply` resolves the original Task in your Session and checks your current assignment. A conflicting `--task` fails. All assignees see the reply; the original sender is mentioned if still assigned. `--no-mention-sender` skips that automatic mention; `--mention NAME` adds others. Retry the unchanged command with the same operation ID after a lost response.
- `task handoff` states the mail range and how many earlier messages are omitted. Its cursor continues after the excerpt; `mail read --task T` starts from the beginning. Neither changes your cursor file.
- The receipt of a send has the message ID in `resource_id` and its sequence number in `revision`.
- `mail read` returns `data.messages[]` (`id`, `sequence`, `sender_kind` for Operator mail, `sender_id`, `sender_name`, `received_at`, `body`, `mention_ids`), `data.cursor` and `data.more`. Give the cursor back with `--cursor "$cursor"` to read only newer messages.
- `mail check` reads all your Tasks at once: `data.tasks[]`, each with `task_id`, `messages`, `cursor` and `more`, and `data.more`.
- `sender_name` is the sender's Agent name. Mail reads include your own messages; find your own ID in `me agent list`.
- Mail with `sender_kind: "operator"`, `sender_name: "Operator"` and an empty `sender_id` is the Operator's answer to the Task's Needs you question. It mentions every assignee. The question is already resolved; do not resolve it again.

Ask for an explicit acknowledgement when a handoff needs confirmation: add `--ack-required` to send or reply, optionally `--ack-from NAME` (repeatable; otherwise all mentions) and `--ack-due 30m`. Check your own `pending_acks` in `mail check`, even when `tasks` is empty; read the message, then use `mail ack --message ID --operation-id YOUR_ID` if you accept it. If you cannot accept it, explain in Task mail instead. Acknowledgements are informational, not proof of completed work or a condition for finishing a Task. Unassignment withdraws an obligation permanently. A handoff includes pending acknowledgements; when `more_acks` is true, inspect each message’s `acks` through paged `mail read`.

### Read only new mail with a cursor file

Always give `mail check` a cursor file in your private folder. The check saves in it how far you have read, and the next check shows only newer mail. The CLI creates the file if it is missing. With a cursor file, the output lists only the Tasks with new mail; `"tasks": []` means there is none.

- If `data.more` is `true`, more mail is waiting: run the same check again.
- Without a cursor file, every check starts again at the first message and stops after 100 messages, so on a busy Task you never see the newest mail. The output then has a `hint`.
- If the check rejects a saved cursor with `invalid_input`, delete the file. The next check starts again at the first message.
- Use one cursor file per Agent. Two checks at once with the same file are safe, but one of them may show its mail again.

## Errors

| `error.code` | Exit | Meaning and what to do |
| --- | --- | --- |
| `invalid_input` | 2 | A flag, ID or text is wrong. The message names it, and may suggest a close command or flag. Fix the command; use a new operation ID. |
| `conflict` | 1 | The revision changed (`current_revision`, `current_progress`), or a question is already open (`current_question`). Read again, rethink, retry with a new operation ID. |
| `operation_conflict` | 1 | This operation ID was already used for a different change. Use a new ID. |
| `operation_expired` | 1 | This ID is older than 30 days. The change was not applied again, and its receipt is no longer available. Read the Task or retained history to establish the result before making any new change; blindly changing the ID could duplicate it. |
| `forbidden` | 1 | You are not allowed, for example you are not an assignee of the Task or not the Orchestrator. |
| `not_found` | 1 | The ID does not exist in your Session. |
| `session_closed` | 1 | The Operator closed the Session. Reads still work. Wait or ask the Operator. |
| `unauthorized` | 1 | The credential file is wrong or was replaced. Ask the Operator. |
| `integration_missing` | 1 | Only from `herdr bind`: Herdr sees no Claude Code, Codex, Pi or OpenCode session in your pane. Tell the Operator the `herdr integration install` command from the message. |
| `rate_limited` | 1 | You stored too much text in a short time, for example in a loop. Wait `retry_after_seconds`, then retry the same command with the same operation ID. Check that you are not repeating a command by mistake. |
| `storage_unavailable` | 1 | The service is not running, not reachable or failed, and `retryable` is `true`. Your change may have been made. Retry the same command with the same operation ID later. If the service stays down, ask the Operator to start it. Never start the service yourself. |

If a response gets lost, send the same command again with the same operation ID and the same flags. For 30 days you get the original receipt, and nothing changes twice. Older IDs return `operation_expired`, never another effect. Read the retained Task history or mail to establish what happened before making a new change.

## Orchestrator commands

Only the current Orchestrator can run these. See [orchestrator.md](orchestrator.md).

```sh
me agent register --name 02-worker --new-credential-file /private/creds/02-worker.json --operation-id orch-register-02-worker
me task create --title 'Build the API' --body-file /your/private/folder/api.md --ticket '#42' --after A --assign 02-worker --assign 01-orchestrator --operation-id orch-task-api-1
me task edit --task T --title 'Build the REST API' --expected-revision R --operation-id orch-edit-api-1
me task assign --task T --agent 02-worker --expected-revision R --operation-id orch-assign-api-02
me task unassign --task T --agent 02-worker --expected-revision R --operation-id orch-unassign-api-02
me dependency add --from A --to B --expected-revision G --operation-id orch-dep-a-b-1
me dependency remove --from A --to B --expected-revision G --operation-id orch-undep-a-b-1
me task archive --task T --expected-revision R --operation-id orch-archive-api-1
me task unarchive --task T --expected-revision R --operation-id orch-unarchive-api-1
```

`task archive` takes only a Finished or Cancelled Task. It hides the Task in the Operator's graph and keeps everything. Task data shows `archived` and `archived_reason`: `command`, `chain` (every dependent is done) or `age` (it ended a day ago).

`task create` links and assigns the new Task in the same operation: `--after A` (repeatable) makes it depend on A, and `--assign` (repeatable) adds an assignee. It needs no revision, and its receipt's `revision` is 1 plus the number of assignees. Without `--after`, while other Tasks are open, it prints a hint on stderr. `--agent` and `--assign` take an Agent name or ID.

`G` is the Session's `graph_revision`: from `me session inspect`, or from the receipt of your last dependency change. `--from A --to B` means that B depends on A.
