# Herdr mention notices

Task mail always works through ordinary CLI checks. Notifications are an optional nudge to check that mail, separate from reading, Task progress and Needs you. Successful input submission does not establish that a model read or acted on anything.

## Setup and supported combinations

Start the service explicitly with an installed Herdr executable to opt in:

```sh
./a-stack serve --state-dir "$state_dir" --herdr-executable /absolute/path/to/herdr --json
```

Without that flag, delivery is disabled and mail remains available. Missing/unsupported integration data leaves references pending with a diagnostic and bounded backoff; it does not reject mail or Task updates. No accounts, background agent launcher or global Herdr settings are configured.

The only enabled combination is **native Codex 0.164.0 with Herdr 0.9.5**, verified in a dedicated disposable Linux fixture on 2026-10-24 (Herdr client/server 0.9.5, protocol 22; actual native `/status`, gpt-6.1-sol/OpenAI/xhigh/Full Access through the existing oc/YOLO launcher). Other harnesses, unknown versions and even another Codex patch version remain mail-only. The optional test-only fixture service permits one exact disposable binding for isolated rechecks. Complete declared/current identity checks remain mandatory for production delivery.

Enroll the recipient using the [lifecycle guide](lifecycle.md). Attach its fresh registration with `agent attach --runtime-file PRIVATE_FILE`; replacing an established binding requires explicit Operator `agent rebind`, including credential rotation. Preserve a private JSON binding with:

```json
{
  "instance_id": "unique-running-instance",
  "herdr_alias": "registered-herdr-alias",
  "endpoint": "/absolute/path/to/herdr.sock",
  "pane_id": "recorded-pane-id",
  "terminal_id": "recorded-terminal-id",
  "native_session_id": "recorded-native-session-id",
  "harness": "codex",
  "harness_version": "0.164.0"
}
```

These are private captured values, not identifiers to guess or copy from another agent. Herdr aliases follow its lowercase name grammar and remain distinct from a-stack Agent names. Before delivery, the adapter requires complete binding fields, an enabled exact declared harness/version, Herdr CLI 0.9.5, and a current metadata match for alias, pane, terminal, harness and native Session ID/source. Working, Idle and Done accept normal queued input; Blocked, Unknown, stopped/missing or changed targets do not. No idle wait is imposed. Herdr's `agent get` reports no observed harness version: `harness_version` is a registration declaration, distinct from dedicated queue-test evidence. Rebinding/upgrade setup must keep that declaration accurate.

The service forces the recorded `HERDR_SOCKET_PATH` and passes explicit target arguments. It removes inherited Herdr Session/pane/machine selectors and unrelated credential variables. Commands are argument arrays: version, scoped `agent get`, and `agent prompt PANE NOTICE` with a five-second process-context deadline. Herdr 0.9.5 permits its CLI `--timeout` only with `--wait`; normal submission uses neither flag. Only bounded allowlisted metadata/error codes are decoded. No terminal text/title/cwd, raw diagnostic, transcript, body, credential or private path is logged or copied into a notice. No pane injection, permission-dialog response, agent rename/start/restart or lifecycle control is used. The prompt is one fixed instruction to run the recipient's own mail check.

## Durable delivery and recovery

The mail transaction stores one body and pending recipient references before signaling the dispatcher after commit. The dispatcher immediately coalesces already-pending references across the recipient's assigned Tasks; it adds no batching delay. One batch captures at most 500 exact message/recipient references plus the recipient's binding revision/hash. A partial unique index permits one submitting batch per Session/Agent. The initial service drain serializes recipients, bounded to four checks per pass; more ready work triggers another pass. New arrivals remain pending for a later batch.

External processes run outside SQLite transactions. After current runtime metadata is checked, the claim and final preflight recheck Session openness, assignment and binding. Removal permanently suppresses references in the same transaction as unassignment. All outcome/recovery writes target only captured references still in `submitting`, preserving terminal suppression and excluding later arrivals. Rejoining/rebinding never revives suppressed or uncertain history notifications.

| State | Meaning |
| --- | --- |
| `pending` | Waiting for an eligible target or a known pre-input retry |
| `submitting` | Exact covered references captured before external input |
| `submitted` | CLI acknowledged input submission; no read/action acknowledgment |
| `uncertain` | Input may have been sent; automatic resend is prohibited |
| `suppressed` | Membership removal invalidated this delivery reference permanently |
| `self` | Retained mention attribution with no notification |

A process start failure or Herdr's explicit pre-input `agent_blocked` response can return captured references to pending. Unknown failures, malformed success, timeout or cancellation after child start become uncertain. Startup and recovery after a failed outcome commit quarantine surviving submitting references as uncertain; fresh pending references can form another batch. Storage failure never acknowledges a successful outcome that was not committed. Recovery does not submit input or infer that an old target is still current; every later eligible delivery verifies the current binding again.

Retry diagnostics use 5, 10, 20, then 30 seconds of backoff, with a five-second bounded wake tick and immediate postcommit wakeups. Closed Sessions pause new claims. Explicit reopen/resumption can make still-pending work eligible; nothing resets terminal references. A notice already past final preflight can finish during a concurrent close/rebind/removal. Herdr 0.9.5 provides no atomic expected-occupant guard between lookup and input, so that residual race remains. Notices stay generic and subsequent mail fetches independently enforce current assignment. See [the tagged prompt contract](https://github.com/herdrdev/herdr/blob/v0.9.5/docs/next/website/src/content/docs/agent-automation.mdx) and ADR4.

## Diagnostics and bounded maintenance

```sh
./a-stack notifications status --state-dir "$state_dir" \
  --workspace "$workspace_id" --session "$session_id" --json
# An Agent selects its private credential and sees only its own delivery state.
# Operator-only maintenance pass, with default 4 / maximum 16 recipient checks:
./a-stack notifications drain --state-dir "$state_dir" \
  --workspace "$workspace_id" --session "$session_id" --limit 4 --json
```

Status defaults to 50 Agents, caps at 100 and accepts `--after` from `data.next`; Operator may select `--agent`. It reports enabled/configured state, safe reason, next-attempt time and per-delivery-state reference counts. An Agent sees only itself; browser credentials cannot use either endpoint. No private binding, message body or unread/read position is returned. The HTTP endpoints are authenticated POST `/v1/notifications/status` and `/v1/notifications/drain` with explicit Workspace/Session and the same fields. Drain is bounded maintenance over durable batches, not a new mail mutation or resend operation; repeating it never resends submitted/uncertain references.

## Measurements and limits

The deterministic fake-executable fixture retained exactly 1,120 messages, 54,300 body bytes, 3,320 mention/batch-reference rows and 29 submitted batches. Twenty isolated sends to one recipient used twenty submissions. A waiting 100-message burst with three recipients used three submissions; a 1,000-message burst with three recipients used six due to the 500-reference batch cap. Later arrivals and multiple Tasks are tested separately. Bodies remain single-copy.

One local run on 2026-10-24 (Intel Core i5-13420H, Linux x86_64, Go 1.27.3, SQLite 3.53.4) after the prompt-argv correction measured isolated send-through-fake-submission p50 10,386 µs / p95 11,474 µs. The 100-message burst drained in 40,682 µs and the 1,000-message burst in 124,654 µs. These include fake process launch/metadata/SQLite work and exclude real harness queuing/model action. They are observations, not a production performance promise.

Physical DB grew from 4,096 to 3,338,240 bytes and WAL from 770,472 to 4,614,432 bytes. Integrity-checked SQLite compact backups grew from 237,568 to 3,059,712 bytes. Read-only Python SQLite 3.54.0 `dbstat` analysis measured growth of 8,192 bytes in batch rows and 471,040 bytes in exact batch-reference rows; the inflight index and three diagnostic rows each remained one 4,096-byte page. Mail message/mention tables grew 258,048/610,304 bytes, their pending/membership indexes 344,064/507,904 bytes and operation receipts 413,696 bytes. No live main-only database copy was used. Exact references and retained attempt metadata have storage cost; no fixed total-retention bound is promised.

The separate root-controlled native fixture verified two completed notice-triggered mail checks using the corrected no-wait argv. While a 45-second foreground work step was active, the working notice steered the native context before that step and the initial turn ended; its own authorized check returned four retained message IDs including the fresh mention. After the prior turn ended, a new idle notice triggered one own check returning all five retained IDs. Neither the initial work nor timer completion triggered a proactive check. This establishes observed working steering and idle delivery for this pair, not universal end-of-turn queueing.

| Completed phase | Receipt stamp → submission record | Claim stamp → submission record | Receipt stamp → notice helper | Own mail CLI |
| --- | ---: | ---: | ---: | ---: |
| Working | 305.790 ms | 302.890 ms | 7,505.239 ms | 4.762 ms |
| Idle | 305.020 ms | 302.462 ms | 3,751.567 ms | 3.413 ms |

These are two individual same-machine UTC wall-clock observations, without a percentile or SLA. Message `received_at` is set inside the send transaction, before commit; claim time is also an in-transaction stamp. Submission record time follows Herdr's response, and the helper timestamp is invocation after the model sees/handles the notice, not exact input arrival. Scheduling and model handling are included; mail CLI duration includes subprocess/HTTP work. None is pure postcommit notification latency. Two earlier corrected submissions had incomplete fixture metadata capture and are excluded from this timing evidence. The initial invalid-argv attempt stayed uncertain across restarts without another submission association. Durable totals were five bodies/mention/batch references and five batches: four submitted, one uncertain. The fixture was verified and closed, with private credentials cleaned.

Normal/race tests use real SQLite/HTTP/CLI and a fake executable for endpoint/argv/environment, batching, concurrent arrival, recipient serialization, target changes, closure/removal, bounded errors, migration rollback and a process killed after simulated input. Exact production predicate tests reject unverified versions/kinds; live fixture proof does not widen the support list automatically.
