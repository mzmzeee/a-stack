# Mention notices

Task mail always works through ordinary mail checks. A mention notice is an optional nudge: when an Agent is mentioned with `@name`, a-stack can tell it through Herdr to check its mail. A notice is separate from reading mail, from Task progress and from Needs you. When Herdr accepts a notice, this does not prove that the model read the mail or acted on it.

## Turn on notices

To turn on notices, start the service with an installed Herdr executable:

```sh
a-stack serve --state-dir "$state_dir" --herdr-executable /absolute/path/to/herdr --json
```

Without this flag, notices are off and mail works as usual. Herdr does not run on Windows; there, `--herdr-executable` and the `herdr` commands stop with an error, and mail works as usual. If the integration data is missing or not supported, the notices stay pending with a diagnostic, and a-stack tries again with a bounded backoff. Mail and Task updates still work. a-stack configures no accounts, no background agent launcher and no global Herdr settings.

## Supported combinations

Notices are turned on for a harness only after a native fixture showed how it queues input. A harness release in the same minor line, from the verified version up, is accepted too:

| Harness | Accepted versions | Verified with |
| --- | --- | --- |
| Codex | 0.164.0 up to, not including, 0.165 | native Codex 0.164.0, 2026-10-24 ([fixture](#native-codex-fixture)) |

Herdr is accepted when its running server reports private protocol 22, the protocol of Herdr 0.9.5 that the fixtures used, and says that the CLI and the endpoint are compatible (`herdr status server --json`). a-stack does not compare the Herdr release string.

All other harnesses, versions outside these lines and other Herdr protocols stay mail-only. That includes Claude Code, Pi and OpenCode, which `herdr bind` can bind, until a native fixture verifies them. In Pi, `/new`, `/resume` and `/fork` change the session file, so Herdr's metadata no longer matches the binding, as with a new Claude Code session ID. A Codex 0.165 needs a new fixture run before it gets notices. A test-only fixture service allows one exact disposable binding, for isolated checks. Production delivery always needs the complete identity checks below.

The Codex fixture had Herdr client and server 0.9.5 with protocol 22, and the actual native `/status` (gpt-6.1-sol, OpenAI, xhigh, Full Access) through the existing oc/YOLO launcher.

## Enroll a recipient

Enroll the recipient as described in [Workspaces, Sessions and Agents](lifecycle.md). Then the Agent binds its new registration to its own Herdr pane. It runs this inside that pane, with its own credential:

```sh
a-stack herdr bind --as agent --credential-file "$credential_dir/worker.json" \
  --workspace "$workspace_id" --session "$session_id" --agent "$worker_id" \
  --expected-revision 1 --operation-id worker-bind-1 --json
```

`herdr bind` needs Herdr's integration for the harness, because only the integration reports the harness's native session to Herdr. Install it once with `herdr integration install claude`, `herdr integration install codex`, `herdr integration install pi` or `herdr integration install opencode`, then start the harness again in its pane.

The command does these steps:

1. It reads `HERDR_ENV`, `HERDR_PANE_ID` and `HERDR_SOCKET_PATH`, which Herdr sets in each pane. Outside a Herdr pane it fails with `invalid_input`.
2. It checks the Herdr server, like a notice does, and runs `herdr agent get` for its own pane.
3. The pane must hold Claude Code, Codex, Pi or OpenCode, with a native session that Herdr's integration reported. For Claude Code, Codex and OpenCode that is the session ID. Herdr's Pi integration reports the absolute path of Pi's session file, and a-stack keeps that path as the native session. Otherwise it fails with `integration_missing`, and the message names the `herdr integration install` command.
4. If the pane's agent has no Herdr name yet, it names it `a-` plus the lowercase a-stack name, cut to 32 characters, with `herdr agent rename`. It renames only its own pane. If Herdr refuses the name, it fails with `conflict`.
5. It runs the harness's own `--version`, such as `codex --version`, and keeps the first `MAJOR.MINOR.PATCH`.
6. It sends the same request as `agent attach`, with the instance ID `herdr:` plus the terminal ID. The binding never goes to a file, and the receipt does not show it.

Give `--herdr-executable` when `herdr` is not on your `PATH`. A retry with the same operation ID returns the original receipt, as long as the pane and the harness version are unchanged. `herdr bind` attaches only a new, unbound registration. To replace a binding that is already set, the Operator must run `agent rebind`, which also rotates the credential. a-stack never reports agent state or sessions to Herdr, because Herdr restarts a pane with a reported session after a server restart.

Without Herdr's integration, or for another tool, you can attach a binding by hand with `agent attach --runtime-file PRIVATE_FILE`. Keep it in a private JSON file like this one:

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

Record these values from the real running Agent. Do not guess them, and do not copy them from another Agent. Herdr aliases follow Herdr's lowercase name rules and are separate from a-stack Agent names.

Before a delivery, the adapter checks all of these:

- All binding fields are present.
- The declared harness and version are in a supported line.
- The Herdr server at the recorded endpoint is running, reports protocol 22, and is compatible with the CLI.
- Herdr's current metadata matches the alias, pane, terminal, harness, and the native session ID and its source.

A target in the state Working, Idle or Done accepts normal queued input. A target that is Blocked, Unknown, stopped, missing or changed does not. a-stack does not wait for the target to be idle.

Herdr's `agent get` does not report the harness version. So `harness_version` is what `herdr bind` read from the harness, or what you declared in the file, and it is separate from the queue-test evidence. When you upgrade the harness to another minor line, the Operator must rebind the Agent.

## How a-stack calls Herdr

The service sets `HERDR_SOCKET_PATH` to the recorded endpoint, and it passes the target explicitly in the arguments. It does not pass on inherited Herdr session, pane or machine selectors or unrelated credential variables. Each command is an argument array: the server check `status server --json`, a scoped `agent get`, and `agent prompt PANE NOTICE`. Each process has a deadline of five seconds. Herdr 0.9.5 allows its CLI `--timeout` only together with `--wait`, so a normal submission uses neither flag.

The notice is always this fixed text:

> New a-stack Task mail mentions you. Run your own a-stack mail check with your registered Workspace, Session and Agent credential.

a-stack decodes only a bounded allowlist of metadata fields and error codes. It never logs terminal text, titles, working directories, raw diagnostics, transcripts, mail bodies, credentials or private paths, and it never puts them in a notice. It does not inject text into panes, answer permission dialogs, rename, start or restart Agents, or control their lifecycle.

## Delivery and recovery

The mail transaction stores the body once, with a pending reference for each mentioned recipient. After the commit, it signals the dispatcher. The dispatcher at once groups the references that are already pending for that recipient, across all of the recipient's Tasks. It adds no batching delay.

- One batch holds at most 500 exact message and recipient references, plus the recipient's binding revision and hash.
- A partial unique index allows only one submitting batch per Session and Agent.
- The service serializes recipients and checks at most four per pass. If more work is ready, it starts another pass.
- Mentions that arrive during a batch stay pending for a later batch.

Herdr commands run outside SQLite transactions. First the dispatcher checks the current runtime metadata. Then the claim and a final preflight check again that the Session is open, that the Agent is still assigned and that the binding has not changed. An unassignment permanently suppresses the references in the same transaction. All outcome and recovery writes change only the captured references that are still `submitting`. So a suppression stays final, and later mentions are not included. A rejoin or a rebind never brings back suppressed or uncertain notices.

| State | Meaning |
| --- | --- |
| `pending` | Waiting for an eligible target, or for a retry after a failure that happened before any input |
| `submitting` | Captured in a batch before the input is sent |
| `submitted` | The Herdr CLI accepted the input. There is no proof that the Agent read or acted. |
| `uncertain` | The input was possibly sent. a-stack never resends it automatically. |
| `suppressed` | The recipient was removed from the Task. This reference will never be delivered. |
| `self` | A mention of the sender itself, kept for attribution, with no notice |

If the process cannot start, or Herdr answers `agent_blocked` before any input, the captured references go back to `pending`. An unknown failure, malformed success output, a timeout or a cancellation after the process started makes them `uncertain`. At startup, and after a failed commit of an outcome, recovery marks the remaining `submitting` references as `uncertain`. New pending references can form another batch.

A storage failure never records a successful outcome that was not committed. Recovery sends no input and does not assume that an old target is still current. Every later delivery checks the current binding again.

After a failure, a-stack waits 5, 10, 20 and then 30 seconds before it tries the target again. A timer wakes the dispatcher every five seconds, and each commit wakes it at once. A closed Session pauses new claims. When the Session is reopened, its pending notices can be sent again. Nothing resets a final state. An Agent without a complete Herdr binding (`binding_incomplete`), or with a harness outside the supported lines (`unsupported_harness`), gets no timed retries: these reasons depend only on the binding. Its notices stay pending until its binding is attached or changed, or the service starts again. Each check would otherwise be a committed write, every 30 seconds for each such Agent.

A notice that has passed the final preflight can still finish during a concurrent close, rebind or removal. Herdr 0.9.5 has no atomic check that the expected Agent is still in the pane between the lookup and the input, so this race remains. That is why notices stay generic, and why each later mail read checks the current assignment again. See the [tagged Herdr prompt contract](https://github.com/herdrdev/herdr/blob/v0.9.5/docs/next/website/src/content/docs/agent-automation.mdx).

## Diagnostics and maintenance

```sh
a-stack notifications status --state-dir "$state_dir" \
  --workspace "$workspace_id" --session "$session_id" --json
# An Agent gives its own private credential and sees only its own delivery state.
# Operator-only maintenance pass, with 4 recipient checks by default and 16 at most:
a-stack notifications drain --state-dir "$state_dir" \
  --workspace "$workspace_id" --session "$session_id" --limit 4 --json
```

`notifications status` lists 50 Agents by default and 100 at most. To get the next page, give the `data.next` value with `--after`. The Operator can select one Agent with `--agent`. An Agent sees only itself. The status shows whether notices are enabled, a safe reason, the time of the next attempt (`-1` while it waits for a new binding), and the number of references in each state. It never shows private bindings, mail bodies or read positions.

`notifications drain` runs one bounded maintenance pass over the durable batches. It does not change mail, and it does not resend anything. If you run it again, it never resends `submitted` or `uncertain` references.

The HTTP endpoints are authenticated `POST /v1/notifications/status` and `POST /v1/notifications/drain`, with explicit Workspace and Session and the same fields. Browser credentials cannot use either endpoint.

## Jump to an Agent's pane

The Operator can show a bound Agent's pane in Herdr:

```sh
a-stack herdr focus --workspace "$workspace_id" --session "$session_id" --agent "$worker_id" --json
```

`--agent` takes the Agent ID, like every other command. The service runs it, because only the service can read bindings, so it needs `--herdr-executable` like notices do. It first checks the binding against Herdr's current metadata, as before a notice, but it also accepts a Blocked agent and a harness outside the supported lines. Then it runs `herdr agent focus PANE`, and `herdr tab focus TAB` if that fails. The answer is `{"focused": "agent"}` or `{"focused": "tab"}`, never the binding. A changed or missing pane gets `integration_unavailable`, and nothing is focused. Only the Operator can run it; the HTTP endpoint is `POST /v1/herdr/focus` with `agent_id`.

## Measurements and limits

### Fake Herdr fixture

A deterministic fixture with a fake Herdr executable retained exactly 1,120 messages, 54,300 body bytes, 3,320 mention and batch reference rows, and 29 submitted batches.

- Twenty separate sends to one recipient used twenty submissions.
- A burst of 100 waiting messages with three recipients used three submissions.
- A burst of 1,000 messages with three recipients used six submissions, because of the cap of 500 references per batch.

Later arrivals and multiple Tasks are tested separately. Bodies are still stored once.

One local run on 2026-10-24 (Intel Core i5-13420H, Linux x86_64, Go 1.27.3, SQLite 3.53.4), after the correction of the prompt arguments, measured these times:

- A single send through the fake submission: 10,386 µs at p50 and 11,474 µs at p95.
- The burst of 100 messages: 40,682 µs to drain.
- The burst of 1,000 messages: 124,654 µs to drain.

These times include the fake process start, the metadata checks and the SQLite work. They do not include real harness queues or model actions. They are observations, not a promise of production performance.

The physical database grew from 4,096 to 3,338,240 bytes, and the WAL from 770,472 to 4,614,432 bytes. Integrity-checked compact SQLite backups grew from 237,568 to 3,059,712 bytes. A read-only `dbstat` analysis with Python's SQLite 3.54.0 measured these increases:

- Batch rows: 8,192 bytes. Exact batch reference rows: 471,040 bytes.
- The inflight index and the three diagnostic rows each stayed at one 4,096-byte page.
- Mail message and mention tables: 258,048 and 610,304 bytes. Their pending and membership indexes: 344,064 and 507,904 bytes. Operation receipts: 413,696 bytes.

No copy of only the live main database was used. Exact references and retained attempt data use storage, and a-stack promises no fixed limit on total retention.

### Native Codex fixture

A separate fixture with a real native Codex, under root control, verified two completed mail checks that a notice started. Both used the corrected arguments without `--wait`.

- Working: while a 45-second foreground work step was active, the notice steered the native context before that step and the first turn ended. The Agent's own authorized check returned four retained message IDs, including the new mention.
- Idle: after the earlier turn ended, a new notice started one check by the Agent itself, which returned all five retained IDs.

Neither the first work step nor the end of the timer started a check without a notice. This shows that steering while working and delivery while idle were observed for this pair. It does not show that all harnesses queue input until the end of a turn.

| Completed phase | Receipt stamp → submission record | Claim stamp → submission record | Receipt stamp → notice helper | Own mail CLI |
| --- | ---: | ---: | ---: | ---: |
| Working | 305.790 ms | 302.890 ms | 7,505.239 ms | 4.762 ms |
| Idle | 305.020 ms | 302.462 ms | 3,751.567 ms | 3.413 ms |

These are two single wall-clock observations in UTC on the same machine, with no percentile and no service level. The message `received_at` is set inside the send transaction, before the commit. The claim time is also set inside its transaction. The submission record time comes after Herdr's response. The helper time is when the model ran the notice helper after it saw the notice, not the exact time that the input arrived. The times include scheduling and model handling, and the mail CLI time includes the subprocess and HTTP work. None of them is a pure notification latency after the commit.

Two earlier corrected submissions had incomplete fixture metadata and are not part of this timing evidence. The first attempt, with invalid arguments, stayed `uncertain` across restarts and was never submitted again. In total, the fixture stored five bodies, five mention and batch references, and five batches: four submitted and one uncertain. The fixture was verified and closed, and its private credentials were removed.

### Claude Code: partial fixture

Claude Code is not in the table yet. On 2026-10-25 a real Claude Code 2.1.310 ran in an isolated Herdr 0.9.5 server (protocol 22) with Herdr's Claude integration hook, against a disposable a-stack service. No prompt was sent to it, so it used no credits. This showed:

- Herdr's Claude hook reports the session at start (`agent_session` with source `herdr:claude` and the Claude session ID), so `herdr agent get` returns harness `claude`, the pane, the terminal and the session.
- `a-stack herdr bind`, run with the pane's `HERDR_*` values, renamed the unnamed pane to `a-02-claude`, read `claude --version` as 2.1.310 and attached the Agent.
- A mention for that Agent stayed `pending` with reason `unsupported_harness` and next attempt `-1`. The pane's screen did not change.
- `a-stack herdr focus` checked the binding and focused the pane.

Before Claude Code goes into the table, a fixture run like the Codex one must show three things with real turns: a notice sent while Claude is working is queued and leads to the Agent's own mail check, a notice sent while it is idle starts one own mail check, and a notice sent while a permission dialog is open does not answer the dialog. That needs several prompts and a signed-in Claude Code.

### Tests

The normal and race tests use real SQLite, HTTP and the CLI, with a fake executable. They cover the endpoint, arguments and environment, batching, concurrent arrivals, the serialization of recipients, target changes, closure and removal, bounded errors, migration rollback, and a process that is killed after simulated input. Tests of the production checks reject unverified versions and kinds. A successful live fixture does not add a combination to the supported list automatically.
