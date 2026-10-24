# Retained Task mail

Each Task has one retained, ordered mail history. Only current assignees can send, read or recover a send receipt. The Orchestrator must be assigned to the Task; Operator and browser credentials grant no mail access. Every command requires explicit `--workspace ID --session ID`, plus an Agent credential. Add the same `--state-dir` to all commands when using custom state.

## Commands and API

These examples use enrolled identities and an assigned Task from the [lifecycle](lifecycle.md) and [Task](tasks.md) guides.

```sh
./a-stack mail send --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --body 'Ready for review, @worker-b.' --mention worker-b \
  --operation-id mail-review-1 --json
./a-stack mail send --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --body-file message.txt --operation-id mail-details-1 --json
./a-stack mail read --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --limit 100 --json
# Save data.cursor, then supply it to continue this Task's history.
./a-stack mail read --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" --task "$task_id" \
  --cursor "$mail_cursor" --json
./a-stack mail check --as agent --credential-file "$worker_a_credential" \
  --workspace "$workspace_id" --session "$session_id" \
  --cursor-file cursors.json --task-limit 50 --limit 100 --json
```

`--body-file -` reads stdin; it conflicts with `--body`. Bodies must be nonblank, valid UTF-8 and at most 65,536 bytes. Larger documents/results use Task file references. Input is rejected, never truncated. `--mention NAME` accepts a name without `@` and may be repeated.

All three endpoints are authenticated `POST` JSON under `/v1/mail/`, using the foundation's instance, Host/Origin and strict JSON checks. Context fields are `workspace_id` and `session_id`.

| Endpoint | Additional request fields | `data` response |
| --- | --- | --- |
| `send` | `task_id`, `operation_id`, `body`, optional `mentions` name array | Compact receipt: message `resource_id`/`body_ref`, sequence in `revision`, `outcome: "sent"` |
| `read` | `task_id`, optional `cursor`, `limit` | `task_id`, `messages`, `cursor`, `more` |
| `check` | optional `cursors` map, `after_task`, `task_limit`, `limit` | `tasks` array of read pages, optional `next_task` |

Each message has `id`, `task_id`, `sequence`, `sender_id`, `received_at`, `body` and sorted resolved `mention_ids`. Sequence increases within one Task. The service derives sender identity and UTC receipt time. Mail does not change Task progress, Task revision or the Session snapshot revision, and its bodies are absent from Task detail/history and browser snapshots.

## Independent continuation

Reads consume nothing. A missing/empty Task cursor starts from the beginning, including for a new or rejoining assignee. Returned cursors identify the last message actually returned; an empty page preserves its position. `more` means more retained messages follow that position. Cursors are opaque, Task-bound keyset positions; malformed, wrong-Task and nonexistent sequence positions fail. Losing a cursor replays history.

Both reads and checks default to 100 messages and accept 1–500. Responses use an encoded data budget of 768 KiB, leaving space below the 1 MiB full response limit; a byte-limited page may contain fewer messages. A maximum-size, JSON-escaped body plus 500 resolved recipients fits in one response. No cursor advances past an omitted message.

`mail check` scans the caller's current assigned Tasks in Task-ID order. It defaults to 50 Tasks and accepts 1–100 through `--task-limit`. Its message limit and byte budget apply to the whole response. `cursors.json` is a JSON object mapping Task IDs to their most recently saved read cursors, for example `{"TASK_ID":"CURSOR"}`. It may be supplied through `--cursor-file -`; the input file is capped at 1 MiB. The CLI never writes it back.

For each response, merge only the returned `tasks[].cursor` entries into your saved map, retaining all other positions. If `next_task` exists, call again with the merged map and `--after-task` set to that token. Continue until `next_task` is absent, then start the next round without `--after-task`. A Task whose page has `more: true` is revisited in that next round, or directly with `mail read`. This keeps aggregate checks bounded while preserving every Task's independent position. Scan continuations are bound to the Session and caller. Tasks removed before a later request are omitted; newly assigned Tasks earlier in the scan appear on the next round. There is no server read ledger, acknowledgment or unread counter.

## Mentions, access changes and retries

Names follow the Agent name grammar: 1–64 ASCII letters/digits/underscores/hyphens, starting with a letter or digit. A body mention starts at the beginning of text, after whitespace, or after opening punctuation (including an opening quote or `<`). It consumes the full name token, so `@worker-b-extra` never targets `worker-b`. Ordinary and quoted-local-part email addresses such as `person@worker-b.example` and `"person"@worker-b.example` do not mention `worker-b`; `Ask "@worker-b"` does. Parsing does not depend on Task membership; every parsed or explicit name must then resolve to a current assignee in the send transaction. Unknown or nonmember names reject the entire send.

Body and explicit mentions are deduplicated. The implementation caps the union at **500 distinct recipients** to bound attribution in a single message response; repeated duplicates do not use additional slots. Resolved Agent IDs are retained, preventing later name reuse from retargeting messages. Self mentions retain attribution with state `self`, without a pending notification.

Assignment removal revokes read/send and matching receipt replay immediately, and suppresses that recipient's existing pending/submitting references in the same transaction. Rejoining restores the full retained history, including the interval away, but never restores those suppressed notices. Sent messages survive removal. Task completion/cancellation does not end mail. Closed Sessions retain authorized reads and matching receipt recovery, and reject new sends.

Generate and retain an operation ID before sending. A same-ID, unchanged request returns the original receipt after a lost response or restart, provided the sender still has current read access. A changed payload conflicts. Retrying after closure can recover a committed send; an uncommitted send remains rejected. The body, sequence, resolved mention references and compact receipt commit together. Failed writes or a process crash before commit leave none of them visible. Reads, membership changes and closure serialize with sends using the existing SQLite transaction boundary.

## Notification handoff for #7

Schema version 4 introduces `mail_messages`, keyed by stable message ID with unique `(task_id, sequence)`, and `mail_mentions`, keyed by `(message_id, recipient_id)` with a composite message/Task foreign key. Each mention row is also a compact durable outbox reference; it contains no body. `NoticeState` values are `pending`, `submitting`, `submitted`, `uncertain`, `suppressed`, plus non-notifying `self`. `state_at` records the last state change. The pending index leads with state/recipient/message; the membership index leads with Task/recipient/state/message.

Mail creates `pending`/`self` references and suppresses pending/submitting references on removal. The optional [notification dispatcher](notifications.md) owns exact durable batch coverage, runtime/binding validation, submission and uncertainty recovery outside mail transactions. `suppressed` remains terminal through an in-flight outcome update or rejoin. Surviving `submitting` references become uncertain on recovery and are never automatically resent. Closed Sessions pause unsent notices; they do not delete references. Generic notices contain no mail body, credential or private path. Fetching mail remains independently authorized even after a prompt is submitted.

## Measured storage and validation

`go test ./internal/stack -run TestMailGrowth10000SingleCopyBodies -count=1 -v` sends 10,000 synthetic 56-byte messages, each mentioning three other assignees. One local run with bundled SQLite 3.53.4 retained exactly **10,000 message/body rows, 560,000 body bytes, 30,000 mention/pending rows and 10,000 send receipts**, with zero submissions. Bodies are stored once regardless of recipient count.

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

Physical sizes include checkpoint/freelist effects; object sizes above come from `dbstat` on integrity-checked SQLite-produced compact backups, read using Python's SQLite 3.54.0. No live database was copied. The sends took 2,521.846 ms overall; core-send p50 was 221 µs and p95 436 µs. These fixture measurements exclude HTTP/CLI and establish no production throughput or latency promise. Indexes and receipt/notice metadata account for substantially more bytes than these short bodies. History and operation IDs have no total-retention cap in v1.

The mail tests use real SQLite/HTTP, CLI subprocesses and temporary state. They cover independent paging, encoded-byte limits with maximum bodies/recipients, late join/rejoin, credential replacement, membership/closure races, concurrent sequences, migration rollback, failed mention/receipt writes, and abrupt process exit after a mention insert followed by recovery. The standard normal/race suites, `go vet ./...` and build apply. Herdr delivery and physical power-loss behavior remain outside this slice.
