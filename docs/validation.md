# Reproduce v1 validation

The [standard-library runner](../scripts/validate-v1.py) exercises the compiled CLI and loopback HTTP service with private disposable state. It uses a strict fake Herdr executable, never a live agent. Start with the [Operator and Agent guide](getting-started.md) for everyday use; this page describes validation and measured limits.

## Run

Use Linux, Python 3, `findmnt`, and the Go/C toolchain described in the [foundation guide](foundation.md). Run from the repository root:

```sh
python3 scripts/validate-v1.py --output .tmp/validation-results.json
```

Every default invocation builds current source to `.tmp/a-stack-v1`, then runs the workflow, five existing engine/recovery tests, and measurements. It prints sanitized JSON and exits nonzero on a failed assertion. The output records the binary SHA-256. To check workflow/recovery without measurements:

```sh
python3 scripts/validate-v1.py --quick
```

To deliberately select an existing binary, pass `--binary /absolute/path/to/a-stack`. That bypasses rebuilding; the caller selects the artifact, whose hash is recorded. The selected Go tests still use current repository source.

Fixtures use Python's temporary-directory location. `/tmp` may be tmpfs; choose a private directory on a persistent **local** filesystem when measuring storage:

```sh
validation_tmp="$(mktemp -d "$HOME/a-stack-validation.XXXXXX")"
TMPDIR="$validation_tmp" python3 scripts/validate-v1.py --output .tmp/validation-results.json
rmdir "$validation_tmp"
```

The runner records the fixture filesystem and `/tmp` separately. On ordinary completion or handled failure it stops/reaps its owned service, closes HTTP connections, and removes temporary state, synthetic projects, credentials and backups. It never selects existing a-stack state or controls native agents. An uncatchable kill of the runner can leave its private fixture for manual inspection/cleanup. Service readiness has a ten-second deadline and 4,096-byte line limit; paginated reads reject empty continuing pages and repeated cursors.

## What is asserted

- Operator enrollment, appointment, a branching dependency graph, shared/unassigned Tasks, graph authority, cycle/self-edge rejection and stale revisions.
- Results, cancellation/reopen, explicit attention independent of progress, retained history and display-only file references.
- Independent Task-mail readers and cursor replay, exact operation-ID retries, changed-payload conflicts, removal/rejoin, current-membership authorization and scoped browser denials.
- Stopped Orchestrator observation and replacement; old-role loss; closed Sessions with unfinished work and retained receipts.
- Missing Herdr, unavailable/uncertain notices, runtime/credential replacement, suppressed references and fresh mentions after uncertainty.
- A real service SIGKILL during fake submission; restart without resending captured references; a proxy that drops the response **after** reading the committed upstream receipt; retry returning the original receipt.
- A completed, populated backup restored into another private state directory with the original Operator credential: IDs, nine messages, cursor replay, 15 Task-history entries, original receipt and old credential revocation survive.

The five selected tests are `TestMutationAtomicFailureAndDiskFull`, `TestReadOnlyStorageFailure`, `TestMailProcessCrashRollbackAndOriginalReceiptRecovery`, `TestNotificationsCrashRecoveryNeverResendsCapturedRefs`, and `TestSafeBackupRestoreAndMissingCredential`. The runner requires actual top-level PASS events from `go test -json`, rejecting skipped/missing tests. These exercise real SQLite FULL/read-only errors and subprocess recovery; they do not reproduce a physical disk outage or power loss. The runner opens only completed backups for read-only SQLite analysis; the service owns the live database.

The primary complete run passed with 320 CLI and 43 HTTP calls, nine single stored message bodies, 15 restored history entries, and notice states pending=2, self=1, submitted=2, suppressed=1, uncertain=2. Call totals can vary with bounded status polling; the retained-data assertions are exact.

Browser rendering, both-theme keyboard behavior, stored/runtime distinctions, malicious text and stable viewport were independently checked against unchanged production assets. The runner checks API capabilities, not browser rendering. See [browser contracts and layout limits](browser.md). The accepted native Codex queue proof and its timing limits are recorded in [Herdr mention notices](notifications.md); this validation does not repeat live prompts or establish a new delivery guarantee.

## Measured results

The primary evidence is the independent mission orchestrator's complete run at frozen candidate `3580aceb04f24c5997481aa26d363cc75771f21a`, retained in [sanitized measured JSON](validation-results.json). It was executed by the orchestrator, not the runner author. Subsequent bounded-readiness/page guards and fixture cleanup changes passed focused checks and a workflow/engine run; these tables were not remeasured after those changes. No production Go/browser code changed in this validation slice.

Machine: Intel Core i5-13420H, Linux x86_64, kernel `7.2.8-9-omarchy`; Go 1.27.3, Python 3.14.8, bundled service SQLite 3.53.4, read-only analysis SQLite 3.54.0. Fixture storage was persistent **btrfs**, while `/tmp` was **tmpfs**. This does not establish physical flush latency or power-loss durability. The strict Python fake declares Herdr 0.9.5 / Codex 0.164.0; it establishes no native queue behavior. The measured binary hash is in the JSON.

Each timing discards two warmups and retains 20 samples. Percentiles use nearest rank (samples 10 and 19 of the sorted 20). External spans use a monotonic clock. Enrollment uses actual CLI subprocesses; bulk fixture creation uses authenticated HTTP and is excluded from CLI latency. Each size has four Agents, a shared Task, unassigned Tasks, mixed progress, two explicit questions, literal markup, reference metadata and a branching graph with N−1 edges.

Times below are milliseconds, p50 / p95:

| Tasks | CLI Task list, first 100 | Changed poll, all collections | HTTP requests per changed poll | Unchanged conditional poll |
| ---: | ---: | ---: | ---: | ---: |
| 10 | 3.893 / 4.169 | 1.989 / 3.798 | 5 | 0.165 / 0.295 |
| 100 | 8.669 / 11.795 | 9.084 / 12.594 | 5 | 0.132 / 0.394 |
| 1,000 | 8.751 / 11.077 | 97.448 / 109.232 | 31 | 0.120 / 0.151 |

A changed poll loads a manifest plus every page of Tasks, Agents, assignments and dependencies pinned to one read revision. It includes HTTP/JSON costs, not browser JavaScript, layout or paint. The first-page CLI measurement is not a complete 1,000-Task listing. Each size also completed 22 authenticated unchanged polls (warmups included), all HTTP 304 with **zero response-body bytes**; request/response headers are excluded from that byte count.

For the 1,000-Task fixture, 10,000 messages of 56 UTF-8 bytes each were written with notices disabled. Each mentions three nonself current assignees: 560,000 body bytes stored once and 30,000 pending references. Starting the fake dispatcher drained them into exactly 60 submissions of 500 covered references. There is no batching timer or live delivery claim. Afterwards, 22 isolated one-recipient CLI sends supplied two warmups and 20 measured samples:

| Span | p50 ms | p95 ms |
| --- | ---: | ---: |
| CLI send start → receipt | 8.573 | 9.126 |
| Send start → observed durable submitted state | 178.965 | 198.285 |
| Batch claim stamp → outcome stamp | 60.661 | 65.027 |

Submitted-state observation polls every 20 ms and includes scheduling, fake process startup and status-command overhead. Batch stamps use wall-clock timestamps **inside** their transactions; their difference is not a pure postcommit span. Neither measure is input consumption, model response time or an SLA. HTTP burst setup duration is separately recorded in the JSON and is not CLI send latency.

## Retained storage

All sizes below are bytes. Live files are a point-in-time observation before a safe backup; WAL/checkpoint timing means the main DB alone does not represent retained data. Completed backups are the service's safe SQLite backups, not copies of the live main file.

| Fixture | Live DB | WAL | SHM | Completed backup |
| --- | ---: | ---: | ---: | ---: |
| 10 Tasks | 4,096 | 2,249,552 | 32,768 | 274,432 |
| 100 Tasks | 847,872 | 4,185,952 | 32,768 | 815,104 |
| 1,000 Tasks | 6,352,896 | 4,214,792 | 32,768 | 5,849,088 |
| +10,000 messages, submitted burst | 34,422,784 | 4,391,952 | 32,768 | 31,019,008 |

The pending-mail backup was 26,402,816 bytes. The submitted backup retains 10,000 message rows, 30,000 mention rows, 14,374 operation receipts, 60 batches and 30,000 batch-reference rows. Body-once storage avoids multiplying the 560,000 body bytes by recipients, but metadata, IDs, receipts, history and indexes remain substantial. It is not a 560 KB database or a bounded-retention claim.

Read-only `dbstat` of the submitted backup reports, for example:

| SQLite object | Allocated bytes |
| --- | ---: |
| `mail_messages` | 2,416,640 |
| Message uniqueness indexes, combined | 1,904,640 |
| `mail_mentions` | 5,550,080 |
| `mail_mentions_membership` index | 4,562,944 |
| `mail_mentions_pending` index | 3,080,192 |
| `operation_receipts` | 4,698,112 |
| `notice_batch_refs` | 4,255,744 |
| `task_history` + its index | 1,015,808 |

These are allocated SQLite pages, including row/index overhead, not raw content sizes. The JSON retains complete per-object page/byte listings for pending and submitted backups. Counts/storage are captured before the 22 isolated timing messages.

## Optional disposable browser fixture

To supply a synthetic ten-Task Session to a browser reviewer, choose a new directory outside project checkouts:

```sh
browser_fixture="$(mktemp -d)"
python3 scripts/validate-v1.py --browser-fixture "$browser_fixture"
```

The foreground command prints only the path to a mode-0600 `browser-context.json`, a stop-marker path and the 600-second lifetime. The private context contains a fragment-authenticated view URL, a scoped browser credential-file path and synthetic IDs. Open the URL privately; do not paste the context or credentials into reports. The view cannot read mail bodies or mutate records. No live Herdr is enabled.

The service lives for at most ten minutes after readiness, or until Ctrl-C, SIGTERM, or creation of `$browser_fixture/stop`. On handled exit it stops/reaps its service and deletes the context, Agent/browser credential directory and Operator credential file. Synthetic SQLite state, backups, project marker, fake configuration and binding metadata remain in that private directory for its owner to inspect and remove. This mode intentionally does not delete the supplied directory. The author's focused check verified the scoped snapshot, private modes, SIGTERM shutdown and credential cleanup; independent browser QA is separate evidence.
