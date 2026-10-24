# Workspace, Session and Agent lifecycle

The service owns stable opaque IDs; the CLI always sends explicit context. A Workspace identifies an existing canonical project directory. `workspace create --path ABSOLUTE_DIR` resolves symlinks and rejects a duplicate canonical path. It never imports the current directory. A Session belongs to one Workspace; an Agent registration belongs to one Session for its lifetime. `agent register --worktree ABSOLUTE_DIR` explicitly associates a separate working directory with that Workspace without creating another Workspace.

Agent names are case-sensitive, unique within the Workspace and match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Canonical directory paths are limited to 1,024 UTF-8 bytes; Session names to 128. IDs and private runtime bindings are distinct from display names and Herdr aliases.

## Enroll a team

Start the service as described in the README. These examples use the default state directory; for temporary/custom state, pass the same `--state-dir ABSOLUTE_DIR` on every command.

```sh
./a-stack workspace create --path /absolute/project --operation-id project-create-1 --json
# Set workspace_id to data.resource_id from that receipt.
./a-stack session create --workspace "$workspace_id" --name "Build" \
  --operation-id session-create-1 --json
# Set session_id to data.resource_id from that receipt.
credential_dir="$(mktemp -d)"
./a-stack agent register --workspace "$workspace_id" --session "$session_id" \
  --name 01-orchestrator --new-credential-file "$credential_dir/orchestrator.json" \
  --operation-id orchestrator-register-1 --json
# Set orchestrator_id to data.resource_id from that receipt.
./a-stack session appoint --workspace "$workspace_id" --session "$session_id" \
  --agent "$orchestrator_id" --expected-revision 1 --operation-id appoint-initial-1 --json
```

A newly created Session explicitly has `orchestrator_id: null`. Only the Operator appoints/replaces it. A single Session column and a same-Session foreign key enforce one designated holder. Appointment advances the Session revision; competing changes with the same expected revision return `conflict` and `current_revision`. Replacement immediately removes the old holder's Orchestrator role while retaining its ordinary Agent identity/permissions. Runtime absence/idle/done never elects a replacement or freezes other workers.

The Operator or current Orchestrator can register a worker in an open Session. For example, using **only** the Orchestrator credential:

```sh
./a-stack agent register --as agent --credential-file "$credential_dir/orchestrator.json" \
  --workspace "$workspace_id" --session "$session_id" --name 02-worker \
  --new-credential-file "$credential_dir/worker.json" --operation-id worker-register-1 --json
```

Give that worker its private `worker.json`, the explicit Workspace/Session/Agent IDs and selected state directory. Keep the Operator credential out of its instructions, reports and ordinary file references. A trusted local process can still access other files owned by the same OS user; these scopes prevent accidental API privilege escalation, not hostile same-user filesystem access.

A worker selects its credential and context on every call:

```sh
./a-stack session inspect --as agent --credential-file "$credential_dir/worker.json" \
  --workspace "$workspace_id" --session "$session_id" --json
./a-stack agent attach --as agent --credential-file "$credential_dir/worker.json" \
  --workspace "$workspace_id" --session "$session_id" --agent "$worker_id" \
  --runtime-id explicit-runtime-instance-id --expected-revision 1 \
  --operation-id worker-attach-1 --json
```

Set `worker_id` from the worker registration receipt. Agent/browser mode requires an explicit matching-kind credential file; it never falls back to the Operator file. Agents can inspect only their registered Session, even if a request supplies another ID. Browser credentials remain denied these lifecycle data routes; the Task snapshot/detail routes explicitly allow scoped reads.

## Credential delivery and recovery

Registration and Operator rebind require `--new-credential-file` in an existing owner-only directory (0700), outside project checkouts. New destinations under a Git checkout are rejected before token generation or file creation, including paths reached through symlink ancestors. The CLI generates a random token, writes and syncs its 0600 file **before** sending the request, and transmits only its SHA-256 hash. The database and receipt never contain that token. A private enrollment record binds the file to database identity, operation ID and exact request digest.

If a response is lost, rerun the **same command**, with the same operation ID, flags, actor credential and new-credential filename. It reads the existing file and returns the original committed receipt without another registration/rotation. The file stays in place on errors, including uncertain network outcomes. Changing the payload under that operation ID conflicts; reusing the private file for a different request/database also conflicts locally. To correct a rejected request, first establish whether it committed, then choose a new operation ID and filename for the new request. Never delete a pending credential solely because the response was lost.

If the Agent token itself is lost, the Operator recovers access with `agent rebind`, a new operation ID, current Agent revision and a **different** new-credential filename. That transaction rotates the hash, invalidates all old Agent tokens and changes/clears the binding while retaining Agent ID and historical attribution. Preserve all delivered private credential files separately from database backups; hashes cannot reconstruct them.

## Runtime bindings

Only the Agent's own credential can attach its fresh unbound registration. Once established, it requires explicit Operator rebind:

```sh
./a-stack agent rebind --workspace "$workspace_id" --session "$session_id" \
  --agent "$worker_id" --expected-revision 2 --runtime-id replacement-instance \
  --new-credential-file "$credential_dir/worker-replacement.json" \
  --operation-id worker-rebind-1 --json
```

Use `--clear-runtime` instead of a replacement runtime to release the established target and leave the Agent unattached with a rotated credential. Rebind must happen in an open Session; reopen first when credential recovery is needed for a closed Session. A conflict rolls back credentials, binding claims, history and receipt together.

`--runtime-file PRIVATE_JSON` may replace `--runtime-id`. It accepts `instance_id` and optional `endpoint`, `pane_id`, `terminal_id`, `native_session_id`, `herdr_alias`, `harness`, `harness_version`. The private file uses the same bounded regular-file reader as credentials. Instance IDs are at most 128 UTF-8 bytes; each optional metadata value at most 512. No binding is returned in list/inspect/receipt output, and no Herdr command is executed.

The service reserves the explicit instance ID, every supplied endpoint/pane pair, endpoint/terminal pair, and native-session ID. Native-session identifiers are treated as globally unique opaque runtime identifiers. Changing a synthetic instance ID or alias cannot reuse an established concrete target in another registration/Session. Reservations are transactionally released/replaced only by Operator rebind. Target metadata remains a caller assertion: partial bindings and instance-only bindings are **unverified**, and none enable automatic delivery. The optional [notification adapter](notifications.md) verifies current harness identity before dispatch; aliases/panes alone never establish Agent identity.

`agent observe --agent ID --expected-revision N --runtime-id ID --runtime-state unknown|working|idle|done|stopped|blocked` records latest metadata using the Operator or that Agent's own credential. It validates current context, binding identity and binding revision inside its transaction. Observations may advance while closed and leave lifecycle/Agent revisions, roles and work unchanged. They overwrite latest observation metadata without appending a domain receipt/history event. `done` is a runtime observation, not Task completion or Agent deletion. No target is released automatically on exit/unknown state.

## Close, reopen and inspect

`session close` and `session reopen` are Operator-only and require `--expected-revision` plus `--operation-id`. `session inspect` supplies the current lifecycle revision, designated holder and closed flag. Same-operation retries return their original receipt after closure; stale new operations conflict. A new operation already at the requested state succeeds at the same revision without another lifecycle event. Closure serializes with guarded mutations; it retains all registrations/history and never starts/stops terminals. Reopening changes no work status or runtime.

`workspace list`, `session list`, `agent list` return `{items, next?}`. Lists default to 50, accept `--limit 1..100`, and resume with `--after ID`. Operator Workspace enumeration is global; Operator Session enumeration requires a Workspace. Agent enumeration/inspection requires both explicit IDs and returns only its own Session. Metadata omits bearer tokens, credential hashes and private bindings. Output and errors follow the foundation JSON/exit contract; there is no implicit context or Agent-name lookup.

HTTP uses `POST /v1/workspace/{create,list}`, `/v1/session/{create,list,inspect,close,reopen,appoint}` and `/v1/agent/{register,list,attach,rebind,observe}` with strict typed JSON requests. The CLI constructs these payloads; no role/actor name is accepted as authority.

## Guards for later slices

Schema v2 upgrades populated v1 databases transactionally, retaining identity, capability hashes and receipts. `ReadSession(ctx, tx, principal, Context)` checks explicit context and current registration/scope, including after closure. `OpenSession` additionally rejects closed Sessions and intentionally has no runtime/Orchestrator-presence dependency. `RequireOrchestrator` derives the current holder from that Session row. Call these inside `Store.Mutate` callbacks, then apply the later resource's own role/membership/reference/revision checks and writes using the same transaction. Digest mismatches return only `operation_conflict` before invoking a command-specific replay callback. Matching retries resolve the original receipt's resource and must pass current read authorization before returning its outcome. Project/worktree canonicalization is prepared before taking the serialized connection; only a new authorized apply consumes a preparation error. A committed retry therefore still recovers its receipt after the original directory disappears.

Session `revision` protects lifecycle/appointment, Agent `revision` protects binding identity, and Session `read_revision` advances for visible membership/binding/observation changes. [Task/graph revisions and assignee permissions](tasks.md) are implemented separately; the general open guard does not grant those permissions. History stores stable actor/resource IDs and scalar event/revision/time, never credential or binding snapshots.
