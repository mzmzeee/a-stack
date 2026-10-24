# Local service foundation

`cmd/a-stack` owns argument parsing and foreground signal handling. `internal/stack` contains the working state, SQLite, HTTP and client code in one package. Schema version 1 introduced database metadata, hashed capabilities and compact operation receipts. Schema version 2 adds the [Workspace/Session/Agent lifecycle](lifecycle.md); version 3 adds [shared Tasks, graph and bounded snapshots](tasks.md); version 4 adds [retained Task mail and mention references](mail.md); version 5 adds [exact notification batches and bounded retry diagnostics](notifications.md).

## State and credentials

`serve` acquires a nonblocking Linux `flock` on `service.lock` before opening `state.db`. The lock file stays in place; its open descriptor holds ownership until HTTP has stopped and SQLite has closed. A conflicting start fails with `conflict`. A killed process releases the kernel lock; a new start lets SQLite recover its WAL. Do not delete the lock or SQLite sidecars.

The state directory is owned by the current OS user and has no group/other access. Database, discovery and credential files are owner-only. Symlink files and unsafe existing permissions are rejected. State inside a Git checkout is rejected, including paths resolved through ancestor symlinks. Use a local filesystem; network filesystems are unsupported.

On first use the service publishes `operator.json` privately and durably before inserting its token hash. A crash between those steps can resume using the same file. Once established, a missing, unsafe or mismatched Operator file stops startup rather than issuing a replacement. Preserve the original credential separately with the backup. Agent credential recovery/rotation uses explicit Operator `agent rebind`; Operator-file recovery still requires the original private file.

`service.json` is atomically replaced and contains the loopback URL, database ID and per-start instance ID. Clients send `X-A-Stack-Instance`; a stale discovery file cannot silently target another service on a reused port. Normal shutdown removes its own discovery file. After abrupt termination, clients report unavailability until an explicit `serve` restart. Tokens are never printed. The database stores SHA-256 hashes of random 256-bit bearer tokens, with capability kind, subject ID and Session scope; credential files store the bearer token.

The threat model is a trusted local OS user. Owner permissions and bearer checks do not isolate against a malicious process running as that same user.

## HTTP and CLI

Only `127.0.0.1` is accepted, with port 0 by default. Every data request requires an instance ID and bearer token. Host must match the actual bound address; an Origin, when present, must match the service URL. No cross-origin access, token query parameters, redirects or proxy environment forwarding are supported. Browser credentials are denied all foundation endpoints; the browser slice must explicitly opt in Session-scoped reads. Agent credentials can check health but cannot inspect global storage or create backups.

| Command | HTTP | Capability |
| --- | --- | --- |
| `health` | `GET /v1/health` | Operator or Agent |
| `storage status` | `GET /v1/storage` | Operator |
| `storage backup --destination ABSOLUTE_FILE` | `POST /v1/storage/backup` with `destination` | Operator |

Flags may appear before or after the command. `serve` supports `--listen 127.0.0.1:PORT`; all commands support `--json` and `--state-dir`. Client commands support `--credential-file` and `--as operator|agent|browser`. `serve` always bootstraps/verifies its own state credential. Domain commands require explicit Workspace/Session IDs; the foundation health/storage commands are service-wide.

JSON bodies are limited to 1 MiB, must be UTF-8, have exactly one value, and reject unknown fields. GET bodies are rejected, including chunked input. Private JSON files are capped at 4 KiB and nonregular files cannot block credential reads. Headers and read/write/idle timeouts are bounded. HTTP failures use `error: {code, message, retryable, current_revision?}`; Task conflicts include the current revision/progress, and competing attention requests include the current question. Messages do not echo input, credentials, paths or SQL. Stdlib request logging is disabled, so there is no unbounded operational log. CLI requests do not retry automatically. Client commands retain normal SIGINT/SIGTERM termination, including while waiting for stdin; only foreground `serve` handles those signals for graceful shutdown. If an interrupted mutation may already have committed, retry its unchanged operation ID/payload to recover the original receipt.

## SQLite and transaction interfaces

A single serialized `database/sql` connection enables WAL, `synchronous=FULL`, foreign keys and a 5,000 ms busy timeout. Transactions use `BEGIN IMMEDIATE`. SQLite schema migration and `user_version`/`application_id` updates commit together; an unsupported/newer/foreign schema is rejected without resetting it. SQLite owns checkpointing and sidecar cleanup. Storage errors return `storage_unavailable`; no successful receipt is returned after a failed commit. A disconnect during commit can leave the outcome unknown, so callers must retry the same operation ID.

Later endpoints in `internal/stack` use these interfaces:

- `IssueCapability(ctx, tx, kind, subjectID, sessionID)` issues a token within registration/rebind's transaction. The server derives `Principal` from the stored hash, never a request-supplied identity. Delete the old hash within the same transaction when revoking/rotating credentials. Kind and Session scope are groundwork, not a substitute for checking current domain membership/roles.
- `Store.Mutate(ctx, token, Operation, authorizeReplay, apply)` authenticates again inside its transaction. IDs contain 8–128 ASCII letters/digits/underscores/hyphens. `Operation` includes name, resource scope and a bounded JSON payload. Handlers should serialize typed payloads deterministically with `json.Marshal`, including expected revision and all mutation parameters. The digest includes name and scope. Arbitrary reordered JSON object keys are not promised to compare equal.
- `apply(ctx, tx, principal)` checks current Session openness, role/membership, references and expected revision, then writes the resource, compact history and any mention references using that transaction. Return a `Receipt` with resource ID, revision, scalar outcome and optional body-reference ID. No external calls, body copies or snapshots belong in a receipt or transaction.
- `authorizeReplay(ctx, tx, principal, receipt)` is mandatory and checks current read access to the receipt's referenced resource. It must allow an authorized receipt read after closure and reject revoked membership. It is called only for a matching digest, before returning a cached outcome. A digest mismatch returns only `operation_conflict` without saved resource data and does not invoke the new command's typed replay callback. A revoked token fails authentication before receipt lookup. Browser credentials cannot call `Mutate`.

Receipts are indexed by stable actor kind/subject and operation ID. Same-ID/same-payload retries return the exact committed receipt; changed name/scope/payload yields only `operation_conflict` after authentication and before typed replay authorization. Matching retries still require current resource read access; a removed member cannot retrieve a prior outcome. Conflicts never return saved IDs, bodies or receipt data. Different actors cannot recover each other's receipts. Operation IDs never expire. Body persistence, history retention and domain-specific authorization/revision rules are implemented in their dependent slices.

`storage status` reports physical main DB/WAL/SHM sizes, which may change between observations. No retained data is purged. The single connection and backup operation serialize work; add read concurrency only after measurement.

## Backup and restore

Backups require a new absolute destination filename in an existing private directory outside application state. The service holds its serialized connection, reauthenticates the Operator, uses SQLite `VACUUM INTO` to create a private staging database, runs `PRAGMA integrity_check`, syncs the file, then publishes the destination without overwriting and syncs its directory. The named destination appears only after a complete checked snapshot. This maintenance operation changes no domain records and has no domain operation receipt: retry to an existing filename returns `conflict`. After an interrupted call, inspect the completed backup or use another destination; abandoned private staging files from a process crash are Operator-managed.

To restore a completed backup, stop the target service first. Use a new empty private directory, and copy only the **completed SQLite-produced backup**, plus its original Operator credential:

```sh
# backup_dir is the private directory used by the backup command.
# Stop the old foreground service with Ctrl-C before switching state.
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/a-stack"
restore_dir="$(mktemp -d)"
install -m 600 "$backup_dir/a-stack.sqlite" "$restore_dir/state.db"
install -m 600 "$state_dir/operator.json" "$restore_dir/operator.json"
./a-stack serve --state-dir "$restore_dir" --json
```

In another terminal, use `./a-stack health --state-dir "$restore_dir" --json` and confirm the original database ID. Never copy a live main database alone, merge a backup with old sidecars, or delete sidecars to attempt recovery. Preserve the old state until the restored copy has been checked. Preserve delivered Agent credential files separately too; database hashes cannot reconstruct their tokens. See [credential delivery and recovery](lifecycle.md#credential-delivery-and-recovery). Restore tests check integrity, database identity, credentials and retained test rows/receipts using real SQLite.

## Validation limits

`go test ./...` covers loopback/auth/parser restrictions, permissions/FIFO rejection, startup conflicts, transactional migrations, foreign/newer schema rejection, crash/restart, retry concurrency and current authorization, precommit/receipt rollback, real `SQLITE_FULL` via SQLite page limits, read-only write failure, and safe backup/restore. `go test -race ./...` runs the same checks with the race detector. Test-only tables exercise mutation primitives; they are absent from production schemas.

The disk-full test exercises the actual SQLite engine's full-database path without filling the host filesystem. Tests do not simulate a physical device failure or power cut. No production latency/throughput claim, domain workload benchmark, browser view or Herdr integration is included in this slice.
