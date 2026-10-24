# Validation

The [validation runner](../scripts/validate-v1.py) tests the compiled CLI and the loopback HTTP service with private, disposable state. It uses only the Python standard library. It uses a strict fake Herdr executable and never a live agent. This page explains how to run it and records the measured results of v1. For everyday use, read [Getting started](getting-started.md) instead.

## Run the runner

You need Linux, Python 3, `findmnt`, and the Go and C toolchain from the [README](../README.md#requirements). Run this command from the repository root:

```sh
python3 scripts/validate-v1.py --output .tmp/validation-results.json
```

By default, the runner builds the current source to `.tmp/a-stack-v1`. It then runs the workflow, five existing engine and recovery tests, and the measurements. It prints sanitized JSON, which includes the SHA-256 hash of the binary. If an assertion fails, it exits with a nonzero code.

To check only the workflow and the recovery tests, without measurements:

```sh
python3 scripts/validate-v1.py --quick
```

To use an existing binary, give `--binary /absolute/path/to/a-stack`. The runner then does not build, and it records the hash of the binary that you chose. The selected Go tests still use the current source.

The fixtures use Python's temporary directory. `/tmp` can be a tmpfs. To measure storage, use a private directory on a persistent local filesystem:

```sh
validation_tmp="$(mktemp -d "$HOME/a-stack-validation.XXXXXX")"
TMPDIR="$validation_tmp" python3 scripts/validate-v1.py --output .tmp/validation-results.json
rmdir "$validation_tmp"
```

The runner records the filesystem of the fixtures and of `/tmp` separately. When it finishes, or when it handles a failure, it stops and reaps its own service, closes HTTP connections, and deletes its temporary state, synthetic projects, credentials and backups. It never uses existing a-stack state and never controls native agents. If the runner is killed with a signal that it cannot catch, its private fixture can stay behind for you to inspect and delete.

The runner waits ten seconds at most for the service to be ready, and the ready line can be 4,096 bytes at most. Paged reads reject an empty page that says more follows, and a repeated cursor.

## What the runner checks

- Operator enrollment and appointment, a branching Task graph, shared and Unassigned Tasks, graph authority, rejection of cycles and self-dependencies, and out-of-date revisions.
- Results, cancel and reopen, Operator questions that are separate from progress, retained history, and file references that are only shown.
- Independent Task mail readers and cursor replay, exact retries with the same operation ID, conflicts for a changed payload, removal and rejoin, authorization by current membership, and rejected browser requests.
- An observation of a stopped Orchestrator and its replacement, the loss of the old role, and closed Sessions with unfinished work and retained receipts.
- A missing Herdr, unavailable and uncertain notices, runtime and credential replacement, suppressed references, and new mentions after an uncertain notice.
- A real SIGKILL of the service during a fake submission, and a restart that does not resend the captured references. A proxy that drops the response after it has read the committed receipt, and a retry that returns the original receipt.
- A completed, populated backup that is restored into another private state directory with the original Operator credential. The IDs, nine messages, cursor replay, 15 Task history entries, the original receipt and the revocation of the old credential all survive.

The five selected tests are `TestMutationAtomicFailureAndDiskFull`, `TestReadOnlyStorageFailure`, `TestMailProcessCrashRollbackAndOriginalReceiptRecovery`, `TestNotificationsCrashRecoveryNeverResendsCapturedRefs` and `TestSafeBackupRestoreAndMissingCredential`. The runner requires real top-level PASS events from `go test -json`, and it rejects skipped or missing tests. These tests cause real SQLite full-disk and read-only errors and recover from subprocess crashes. They do not reproduce a physical disk failure or a power loss. The runner opens only completed backups, read-only, for SQLite analysis. The service owns the live database.

The main complete run passed with 320 CLI calls and 43 HTTP calls, nine message bodies stored once, 15 restored history entries, and these notice states: pending=2, self=1, submitted=2, suppressed=1, uncertain=2. The call totals can change because of bounded status polling. The assertions about retained data are exact.

The runner checks the API capabilities. It does not check browser rendering. Browser rendering, keyboard behavior in both themes, the difference between stored and runtime data, malicious text and a stable view were checked separately against the unchanged production assets. See [Browser view](browser.md). The accepted proof with native Codex and its timing limits are in [Mention notices](notifications.md). This validation does not repeat live prompts, and it gives no new delivery guarantee.

## Measured results

The main evidence is one complete run by the independent mission orchestrator at the frozen candidate commit `3580aceb04f24c5997481aa26d363cc75771f21a`. The orchestrator ran it, not the author of the runner. The [sanitized measured JSON](validation-results.json) keeps the results. Later changes to the readiness and page guards and to fixture cleanup passed focused checks and a workflow and engine run, but these tables were not measured again after those changes. No production Go or browser code changed during this validation work.

The machine was an Intel Core i5-13420H with Linux x86_64 and kernel `7.2.8-9-omarchy`. The software was Go 1.27.3, Python 3.14.8, the bundled SQLite 3.53.4 in the service, and SQLite 3.54.0 for read-only analysis. The fixtures were on persistent btrfs storage, and `/tmp` was tmpfs. This run does not measure physical flush latency or durability after power loss. The strict Python fake declares Herdr 0.9.5 and Codex 0.164.0. It proves nothing about native queue behavior. The JSON has the hash of the measured binary.

Each timing drops two warmup samples and keeps 20 samples. Percentiles use the nearest rank: samples 10 and 19 of the 20 sorted samples. External spans use a monotonic clock. Enrollment uses real CLI subprocesses. Bulk fixture data is written through authenticated HTTP and is not part of the CLI latency. Each size has four Agents, a shared Task, Unassigned Tasks, mixed progress, two explicit questions, literal markup, reference metadata, and a branching graph with N−1 dependencies.

The times are in milliseconds, p50 / p95:

| Tasks | CLI Task list, first 100 | Changed poll, all collections | HTTP requests per changed poll | Unchanged conditional poll |
| ---: | ---: | ---: | ---: | ---: |
| 10 | 3.893 / 4.169 | 1.989 / 3.798 | 5 | 0.165 / 0.295 |
| 100 | 8.669 / 11.795 | 9.084 / 12.594 | 5 | 0.132 / 0.394 |
| 1,000 | 8.751 / 11.077 | 97.448 / 109.232 | 31 | 0.120 / 0.151 |

A changed poll loads the snapshot and every page of Tasks, Agents, assignments and dependencies for one read revision. It includes the HTTP and JSON costs, but not browser JavaScript, layout or paint. The CLI measurement reads only the first page, so for 1,000 Tasks it is not a complete list. Each size also did 22 authenticated unchanged polls, with the warmups. All of them returned HTTP 304 with zero bytes of response body. Request and response headers are not part of that byte count.

For the 1,000-Task fixture, 10,000 messages of 56 UTF-8 bytes each were written with notices off. Each message mentions three current assignees other than the sender. So 560,000 body bytes were stored once, with 30,000 pending references. When the fake dispatcher started, it sent them in exactly 60 submissions of 500 references each. There is no batching timer, and this is no claim about live delivery. After that, 22 separate CLI sends to one recipient gave two warmups and 20 measured samples:

| Span | p50 ms | p95 ms |
| --- | ---: | ---: |
| CLI send start → receipt | 8.573 | 9.126 |
| Send start → observed durable submitted state | 178.965 | 198.285 |
| Batch claim stamp → outcome stamp | 60.661 | 65.027 |

The submitted state is polled every 20 ms. The time includes scheduling, the start of the fake process and the status command. The batch stamps are wall-clock times set inside their transactions, so their difference is not a pure span after the commit. None of these values measures input consumption or model response time, and none is a service level. The JSON records the duration of the HTTP burst setup separately. That duration is not a CLI send latency.

## Retained storage

All sizes in this section are bytes. The live files are a single observation before a safe backup. Because of WAL and checkpoint timing, the main database file alone does not show all retained data. The completed backups are the service's safe SQLite backups, not copies of the live main file.

| Fixture | Live DB | WAL | SHM | Completed backup |
| --- | ---: | ---: | ---: | ---: |
| 10 Tasks | 4,096 | 2,249,552 | 32,768 | 274,432 |
| 100 Tasks | 847,872 | 4,185,952 | 32,768 | 815,104 |
| 1,000 Tasks | 6,352,896 | 4,214,792 | 32,768 | 5,849,088 |
| +10,000 messages, submitted burst | 34,422,784 | 4,391,952 | 32,768 | 31,019,008 |

The backup with pending mail was 26,402,816 bytes. The backup after submission has 10,000 message rows, 30,000 mention rows, 14,374 operation receipts, 60 batches and 30,000 batch reference rows. Because each body is stored once, the 560,000 body bytes are not multiplied by the number of recipients. But the metadata, IDs, receipts, history and indexes still use a lot of space. The database is not 560 KB, and a-stack makes no claim of bounded retention.

A read-only `dbstat` of the backup after submission reports, for example:

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

These are allocated SQLite pages, including row and index overhead. They are not raw content sizes. The JSON keeps the full page and byte lists of each object for the pending and the submitted backup. The counts and storage values were taken before the 22 separate timing messages.

## Disposable browser fixture

To give a browser reviewer a synthetic Session with ten Tasks, choose a new directory outside project checkouts:

```sh
browser_fixture="$(mktemp -d)"
python3 scripts/validate-v1.py --browser-fixture "$browser_fixture"
```

The command stays in the foreground. It prints only the path of a `browser-context.json` file with mode 0600, the path of a stop marker, and the lifetime of 600 seconds. The private context file holds a view URL with the secret in its fragment, the path of a scoped browser credential file, and synthetic IDs. Open the URL in private. Do not paste the context or the credentials into reports. The view cannot read mail bodies or change records. No live Herdr is turned on.

The service runs for ten minutes at most after it is ready, or until you press Ctrl-C, send SIGTERM, or create the file `$browser_fixture/stop`. When it stops normally, it stops and reaps its service, and deletes the context file, the Agent and browser credential directory, and the Operator credential file. The synthetic SQLite state, backups, project marker, fake configuration and binding metadata stay in the private directory, for its owner to inspect and delete. In this mode, the runner does not delete the directory that you gave it. The author's focused check verified the scoped snapshot, the private file modes, shutdown on SIGTERM and the removal of credentials. Independent browser QA is separate evidence.
