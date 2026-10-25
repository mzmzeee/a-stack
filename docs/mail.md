# Task mail

Each Task has one ordered Task mail history, and a-stack keeps all of it. Only the current assignees of the Task can send mail, read mail or get back the receipt of a send. The Orchestrator must be assigned to the Task too. The Operator credential gives no mail access. A browser view credential can only read the Task mail of its own Session, as described in [Browser reads](#browser-reads).

Every mail command needs `--workspace ID --session ID` and an Agent credential. If you use a different state directory, add the same `--state-dir` to every command.

## Commands

These examples use the Agents and the assigned Task from [Workspaces, Sessions and Agents](lifecycle.md) and [Tasks and the Task graph](tasks.md).

```sh
a-stack mail send --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --body 'Ready for review, @worker-b.' --mention worker-b \
  --operation-id mail-review-1 --json
a-stack mail send --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --body-file message.txt --operation-id mail-details-1 --json
a-stack mail read --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --limit 100 --json
# Save data.cursor. Give it in the next read to continue this Task's history.
a-stack mail read --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --cursor "$mail_cursor" --json
a-stack mail check --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" \
  --cursor-file cursors.json --task-limit 50 --limit 100 --json
```

`--body-file -` reads the body from stdin. You cannot use it together with `--body`. A body must not be blank, must be valid UTF-8 and can be 65,536 bytes at most. For larger documents or results, use Task file references. a-stack rejects a body that is too long. It never cuts it. Mail counts toward the sender's [write budget](foundation.md#write-budget): about 127 sends of maximum size at once, then about 16 a minute. `--mention NAME` takes a name without `@`. You can repeat it.

## HTTP API

All three endpoints take authenticated `POST` JSON under `/v1/mail/`. They use the same instance, `Host`, `Origin` and strict JSON checks as the rest of the [HTTP API](foundation.md#http-rules). The context fields are `workspace_id` and `session_id`.

| Endpoint | Other request fields | `data` in the response |
| --- | --- | --- |
| `send` | `task_id`, `operation_id`, `body`, optional `mentions` array of names | A compact receipt: the message `resource_id` and `body_ref`, the sequence number in `revision`, and `outcome: "sent"` |
| `read` | `task_id`, optional `cursor` and `limit` | `task_id`, `messages`, `cursor`, `more` |
| `check` | optional `cursors` map, `after_task`, `task_limit` and `limit` | A `tasks` array of read pages, and `next_task` when more Tasks follow |

Each message has `id`, `task_id`, `sequence`, `sender_id`, `sender_name`, `received_at`, `body` and `mention_ids`. `sender_name` is the sender's current Agent name. `mention_ids` is the sorted list of the resolved Agent IDs. The sequence increases inside one Task. The service sets the sender and the UTC receipt time.

Mail does not change the Task's progress or the Task revision. A send raises the Session read revision, because the Task data has `mail_count`, the number of messages. Mail bodies never appear in Task details, Task history or browser snapshots.

### Browser reads

The [browser view](browser.md#task-mail) credential can use `read` for any Task of its own Session, also a Task with no assignee. It gets the same messages, cursor and limits as an assignee. It cannot use `send` or `check`, and it gets `forbidden` for another Session. A browser read stores no read position, writes no receipt and makes no notice. The Operator CLI credential still has no mail access.

## Read positions

A read does not remove or mark anything. If a Task has no cursor or an empty one, the read starts at the first message. This is also true for a new assignee or an assignee that rejoins.

A returned cursor points to the last message that the response actually contains. An empty page keeps the same position. `more: true` means that more retained messages follow that position. Cursors are opaque keyset positions for one Task. a-stack rejects a malformed cursor, a cursor of a different Task and a position that does not exist. If you lose a cursor, the next read starts again from the first message.

`mail read` and `mail check` return 100 messages by default. Use `--limit` with a value from 1 to 500. A response can hold 768 KiB of encoded data, which leaves space below the response limit of 1 MiB. So a page can contain fewer messages when they are large. A body of maximum size with JSON escapes and 500 resolved recipients fits in one response. A cursor never moves past a message that the response does not contain.

### Check all your Tasks

`mail check` reads the caller's current Tasks in Task ID order. It reads 50 Tasks by default. Use `--task-limit` with a value from 1 to 100. The message limit and the size limit apply to the whole response.

`cursors.json` is a JSON object that maps each Task ID to the last cursor that you saved for it, for example `{"TASK_ID":"CURSOR"}`. You can also give it with `--cursor-file -`. The file can be 1 MiB at most. The CLI never writes the file. `mail check` checks a cursor when it reads that Task, and it ignores the cursors of Tasks that you are no longer assigned to.

After each response, follow these steps:

1. Merge only the returned `tasks[].cursor` values into your saved map. Keep all other positions.
2. If the response has `next_task`, run the check again with the merged map and `--after-task` set to that value.
3. Repeat until the response has no `next_task`.
4. Start the next round without `--after-task`.

If a Task's page has `more: true`, the next round reads more of it. You can also read it directly with `mail read`. This keeps each check bounded, and each Task keeps its own position.

A scan continuation belongs to one Session and one caller. Tasks that you were removed from in between do not appear. If you are assigned a new Task that comes earlier in the scan order, it appears in the next round. The server keeps no read records, acknowledgments or unread counts.

## Mentions

A mention name follows the Agent name rules: 1 to 64 ASCII letters, digits, underscores or hyphens, and it starts with a letter or digit.

In a body, a mention starts with `@`. The `@` must be at the start of the text, after white space, or after opening punctuation such as an opening quote or `<`. The mention takes the full name, so `@worker-b-extra` never mentions `worker-b`. Email addresses do not mention anyone, also with a quoted local part. For example, `person@worker-b.example` and `"person"@worker-b.example` do not mention `worker-b`, but `Ask "@worker-b"` does.

The parser does not use Task membership. After parsing, each name from the body and from `--mention` must be a current assignee of the Task. Otherwise a-stack rejects the whole send. The error names each mention that is not an assignee and lists the current assignee names, for example `mention target must be a current Task assignee: @ghost is not a current assignee of this Task; assignees: 02-worker, 03-worker; write other names without @`. It lists 20 names at most.

Names from the body and from `--mention` are counted once each. One message can mention 500 different recipients at most, so that the attribution in a response stays bounded. Repeated names do not use more places. a-stack stores the resolved Agent IDs. So if a name is used again later, old messages still point to the original Agent. A mention of yourself is stored with the state `self` and makes no notice.

## Access changes

When an Agent is unassigned from a Task, it loses read access, send access and receipt replay for that Task immediately. In the same transaction, a-stack suppresses that Agent's `pending` and `submitting` notices for the Task. If the Agent rejoins, it can read the full history again, including the messages from while it was away. Its suppressed notices stay suppressed. Messages that it sent stay in the history.

Finishing or cancelling a Task does not end its mail. A closed Session still allows authorized reads and the recovery of matching receipts, but it rejects new mail.

## Retries

Choose an operation ID before you send, and keep it. If a response is lost, or the service restarts, send the same unchanged request with the same ID. It returns the original receipt, if the sender still has read access. A changed payload causes a conflict. After the Session is closed, a retry can still recover a committed send, but a send that was never committed stays rejected.

The body, the sequence number, the resolved mentions and the compact receipt commit together. If a write fails, or the process crashes before the commit, none of them becomes visible. Reads, membership changes and closure run in order with sends, inside the SQLite transaction.

## Storage for notices

Schema version 4 adds two tables:

- `mail_messages` has one row per message, with a unique `(task_id, sequence)`.
- `mail_mentions` has one row per message and recipient, with a foreign key to the message and its Task. Each row is also a compact durable outbox entry for a notice. It contains no body.

A mention row has a notice state: `pending`, `submitting`, `submitted`, `uncertain` or `suppressed`, or `self`, which makes no notice. `state_at` records the time of the last state change. The pending index starts with the state, the recipient and the message. The membership index starts with the Task, the recipient, the state and the message.

Mail makes `pending` and `self` rows. An unassignment suppresses `pending` and `submitting` rows. The optional [notice dispatcher](notifications.md) does everything else outside the mail transactions: exact batches, runtime and binding checks, submission and recovery from uncertain outcomes.

- `suppressed` is final, also when a submission is in progress or the Agent rejoins.
- After a crash, rows that are still `submitting` become `uncertain`. a-stack never resends them automatically.
- A closed Session pauses notices that were not sent. It does not delete the rows.
- A notice is generic. It contains no mail body, credential or private path.
- Even after a notice is submitted, the Agent's mail read is authorized separately.

## Measured storage

`go test ./internal/stack -run TestMailGrowth10000SingleCopyBodies -count=1 -v` sends 10,000 synthetic 56-byte messages. Each message mentions three other assignees. One local run with the bundled SQLite 3.53.4 retained exactly 10,000 message rows with their bodies, 560,000 body bytes, 30,000 pending mention rows and 10,000 send receipts. Nothing was submitted. Each body is stored once, however many recipients it has.

| Storage | Before (bytes) | After (bytes) |
| --- | ---: | ---: |
| Physical main database | 4,096 | 23,334,912 |
| WAL | 642,752 | 4,247,752 |
| SHM | 32,768 | 32,768 |
| Completed compact backup | 212,992 | 20,783,104 |
| Message table | 4,096 | 2,416,640 |
| Mention table | 4,096 | 5,365,760 |
| Mention membership index | 4,096 | 4,403,200 |
| Mention pending index | 4,096 | 3,080,192 |
| Message Task/sequence index | 4,096 | 962,560 |
| Message ID/Task index | 4,096 | 942,080 |
| Operation receipts | 4,096 | 3,428,352 |

The physical sizes include checkpoint and freelist effects. The object sizes come from `dbstat` on integrity-checked compact backups that SQLite made, read with Python's SQLite 3.54.0. No live database was copied.

The sends took 2,521.846 ms in total. The core send took 221 µs at p50 and 436 µs at p95. These fixture measurements do not include HTTP or the CLI, and they make no promise about production throughput or latency. For short bodies like these, the indexes, receipts and notice data use much more space than the bodies. History and operation IDs have no total retention limit in v1.

## Tests

The mail tests use real SQLite and HTTP, CLI subprocesses and temporary state. They cover:

- Independent paging, and size limits with maximum bodies and recipients.
- Late join and rejoin, and credential replacement.
- Races with membership changes and closure, and concurrent sequence numbers.
- Migration rollback, and failed mention and receipt writes.
- An abrupt process exit after a mention insert, and the recovery after it.

The normal and race test suites, `go vet ./...` and the build all apply. Herdr delivery and physical power loss are not part of these tests.
