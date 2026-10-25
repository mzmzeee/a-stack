# Local service

The `a-stack` binary is both the command line and the local service. `serve` starts the service in the foreground. All other commands are clients: they read the service's discovery file and a credential file, and then send HTTP requests to the service. A client never starts a service and never opens the SQLite database. The Operator commands `a-stack`, `a-stack -r` and `a-stack new` are the exception: when no service answers for the state directory, they run the service in the foreground exactly like `serve`, and then act as its client. They never start a service in the background.

In the source, `cmd/a-stack` parses the arguments and handles signals for `serve`. The `internal/stack` package holds the state, SQLite, HTTP and client code.

This page describes the state directory, credentials, the command line, the HTTP rules, SQLite, and backup and restore. To build and start the service, see the [README](../README.md).

## State directory

The state directory holds these files:

| File | Contents |
| --- | --- |
| `state.db` | The SQLite database. SQLite also makes the sidecar files `state.db-wal` and `state.db-shm`. |
| `service.lock` | The lock that shows which process owns the state directory. |
| `service.json` | The discovery file for clients. |
| `operator.json` | The Operator credential. |

Before `serve` opens `state.db`, it takes a nonblocking Linux `flock` on `service.lock`. The lock file stays in place. The service holds the lock until HTTP has stopped and SQLite has closed. A second `serve` on the same directory fails with `conflict`. If the process is killed, the kernel releases the lock, and the next start lets SQLite recover its write-ahead log (WAL). Do not delete the lock file or the SQLite sidecar files.

The state directory must belong to the current operating system account and give no access to group or others. The database, discovery and credential files must be private to that account too. a-stack rejects symlinked files and unsafe permissions. It also rejects a state directory inside a Git checkout, also when a symlink in a parent directory leads there. Use a local filesystem. Network filesystems are not supported.

## Credentials

A credential file holds a bearer token. The database stores only a SHA-256 hash of each token, with the capability kind, the subject ID and the Session scope. Tokens are random 256-bit values. a-stack never prints them.

There are three kinds of credentials:

- The Operator credential manages Workspaces, Sessions and Agents.
- An Agent credential belongs to one registered Agent in one Session.
- A browser credential gives read-only access to one Session. See [Browser view](browser.md).

On the first start, the service writes `operator.json` privately and durably. Only then does it store the token hash. If the service crashes between these two steps, the next start uses the same file. After that, if `operator.json` is missing, unsafe or does not match, the service does not start. It never makes a replacement. Keep a copy of the original Operator credential with your backups. Only that original file can recover Operator access.

To recover or rotate an Agent credential, the Operator uses `agent rebind`. See [Workspaces, Sessions and Agents](lifecycle.md#credential-delivery-and-recovery).

a-stack trusts the local operating system account. File permissions and bearer checks do not protect against a malicious process that runs as the same account.

## Discovery

`service.json` holds the service's loopback URL, the database ID and an instance ID that changes on each start. The service replaces the file atomically. Clients send the instance ID in the `X-A-Stack-Instance` header. Because of this, an old discovery file cannot send requests to a different service that now uses the same port.

On a normal shutdown, the service deletes its own discovery file. After an abrupt stop, clients report that the service is unavailable until you start `serve` again.

Without `--listen`, `serve` first tries the port it used the last time it ran without `--listen`. It keeps that port in the private file `last-port.json` in the state directory. If the port is taken, the service picks a free port, records that one, and prints one line on stderr that names both ports. So after a restart, an open browser tab usually finds the service again at the same address. The new instance ID and the bearer checks still apply, and a browser credential still belongs to the database and its Session, not to the port.

Before `serve` says it is ready, it runs SQLite `PRAGMA quick_check` on the state database. If the check fails, `serve` stops with exit code 4 and a message that names the database file and points to [Backup and restore](#backup-and-restore). It never starts on a damaged database. The check reads the whole file: on a 940 MB database it added about 0.2 seconds to the start.

When `serve` is ready, it prints one JSON line on stdout with `status`, `url`, `database_id` and `version`. `health` returns `database_id`, `sqlite_version`, `schema_version` and the `version` of the running service. After an update, compare it with `a-stack version` to see if the service still runs the old binary.

## Command line

Flags can come before or after the command. All commands accept `--json` and `--state-dir DIR`. `a-stack` without a command, `a-stack -r` and `a-stack new [NAME]` also accept `--no-browser`, and take no credential or `serve` flags. They are described in [Open the last Session](browser.md#open-the-last-session). `serve` also accepts `--listen 127.0.0.1:PORT` (port 0 picks a free port; without the flag, see [Discovery](#discovery)) and `--herdr-executable FILE` (see [Mention notices](notifications.md)). Client commands also accept `--credential-file FILE` and `--as operator|agent|browser`.

`serve` makes or checks its own Operator credential, and it does not accept credential flags. A client without `--credential-file` uses `operator.json` in the state directory. `--as agent` and `--as browser` require `--credential-file`. a-stack rejects a credential file of a different kind, and it never uses the Operator credential instead.

Workspace, Session, Agent, Task and mail commands need explicit Workspace and Session IDs. `health` and the `storage` commands apply to the whole service:

| Command | HTTP | Credential |
| --- | --- | --- |
| `health` | `GET /v1/health` | Operator or Agent |
| `storage status` | `GET /v1/storage` | Operator |
| `storage backup --destination ABSOLUTE_FILE` | `POST /v1/storage/backup` with `destination` | Operator |

`a-stack --help` (also `-h` or `help`) lists the commands in groups. `a-stack help COMMAND` or `a-stack COMMAND --help` lists the flags of one command and marks the required ones. The flag list comes from the same table that rejects flags a command does not accept. For a first word such as `task`, the help lists the commands that start with it. `--version` works like `version`. With `--json`, help prints the same `{"data":{"commands":[...],"flags":[...]}}` list for every command.

Every command except `open`, `update`, `skill install`, `skill uninstall`, the three Operator commands and `--help` prints a JSON `data` object on stdout, also without `--json`. Those commands print plain lines without `--json`. If a command fails, it prints a JSON `error` object on stderr and nothing on stdout. The exit code is 0 for success, 2 when the error code is `invalid_input`, and 1 for all other errors. Two exceptions, for `serve` and the Operator commands that start it:

- Exit code 3: another `serve` already owns the state directory and does not answer them. The error code is still `conflict`.
- Exit code 4: the state database cannot be used as it is. It failed the integrity check, or it has a newer or unknown schema (see [SQLite](#sqlite)). The error code is `storage_unavailable` with `retryable: false`. A restart cannot help, so the systemd unit does not restart after exit codes 3 and 4.

The CLI never retries a request automatically. Client commands stop normally on SIGINT and SIGTERM, also while they wait for stdin. Only the foreground service catches these signals, so that it can shut down cleanly. The `a-stack -r` picker also catches them, restores the terminal and then ends the same way. If an interrupted change was possibly committed, send it again with the same operation ID and payload to get the original receipt.

## HTTP rules

The service listens only on `127.0.0.1`. Every data request needs the instance ID and a bearer token. The `Host` header must match the bound address. If a request has an `Origin` header, it must match the service URL. The service has no cross-origin access, and it rejects query parameters, so a token can never be in a query string. Clients do not follow redirects and do not use proxy settings from the environment.

A browser credential can use only the Session read endpoints and `POST /v1/mail/read` for the Tasks of its Session. It cannot use the `health` or `storage` endpoints. An Agent credential can use `health`, but it cannot read storage status or make backups.

Request rules:

- A JSON body can be 1 MiB at most. It must be UTF-8 and contain exactly one value. Unknown fields are rejected.
- A `GET` request with a body is rejected, also with chunked encoding.
- Private JSON files, such as credentials, can be 4 KiB at most. A file that is not a regular file cannot block a credential read.
- Header size and the read, write and idle timeouts have limits.

An error response is a JSON `error` object with `code`, `message` and `retryable`. It also has `current_revision` when that applies. A Task conflict has the current revision and `current_progress`. A second Operator question on the same Task also has `current_question`. Error messages never repeat the input, credentials, paths or SQL. Only the start errors of `serve` with exit code 4 name the state database file, on the Operator's own terminal. Standard request logging is off, so the service keeps no log that can grow without limit.

## SQLite

The service uses one serialized `database/sql` connection with WAL, `synchronous=FULL`, foreign keys and a busy timeout of 5,000 ms. Transactions use `BEGIN IMMEDIATE`. Each schema migration commits together with its `user_version` and `application_id` updates. The service rejects a newer, unsupported or foreign schema and does not reset it. SQLite does its own checkpoints and sidecar cleanup.

A database with a newer schema comes from a newer a-stack, for example after an update to an older version. `serve` then stops with exit code 4. The message names the schema version it found and the newest one it supports. To get back, update a-stack to the newer version with `a-stack update`, or restore a backup made with this version.

Before a migration, `serve` copies the database to `state.db.pre-vN` in the state directory, where N is the old schema version. It uses SQLite `VACUUM INTO`, the file is private (0600), and it is synced to disk. It never replaces an earlier copy: the next copy with the same version is `state.db.pre-vN.2`, and so on. If the copy cannot be written, for example because the disk is full, `serve` stops and does not migrate. The copy is a normal backup: to go back to the older a-stack, restore it as in [Backup and restore](#backup-and-restore). Changes made after the migration are not in it. a-stack never deletes these copies; delete them yourself when you no longer need them.

Each schema version added one area:

1. Database metadata, hashed capabilities and compact operation receipts.
2. [Workspaces, Sessions and Agents](lifecycle.md).
3. [Tasks, the Task graph and bounded snapshots](tasks.md).
4. [Task mail and mention references](mail.md).
5. [Notice batches and bounded retry diagnostics](notifications.md).
6. [Archived Tasks and Operator entries in the Task history](tasks.md#archived-tasks).

A storage error returns `storage_unavailable`. A failed commit never returns a successful receipt. If the connection drops during a commit, the outcome is unknown, so the caller must retry with the same operation ID. The CLI marks this case `retryable: true`: when the service is not running, cannot be reached, or the response was cut off.

`storage status` reports the current sizes of the main database, WAL and SHM files. These sizes can change between two calls. a-stack never deletes retained data. The single connection and the backup operation run work one at a time. Add read concurrency only after a measurement shows the need.

## Operation IDs and receipts

Commands that change records take an `--operation-id` of 8 to 128 ASCII letters, digits, underscores or hyphens. (`open` makes its own operation ID. `agent observe` and `storage backup` take none.) The service stores a compact receipt for each change, under the actor's kind and subject and the operation ID.

- A retry with the same ID and the same payload returns the exact receipt that was committed. It has no new effect.
- A retry with the same ID but a different name, scope or payload returns only `operation_conflict`. A conflict never returns saved IDs, bodies or receipt data.
- A matching retry still needs current read access to the resource. An Agent that was removed cannot get an earlier outcome.
- One actor cannot get another actor's receipts.
- Operation IDs never expire.

## Backup and restore

A backup needs a new absolute file name in a private directory that already exists, outside the state directory. The service holds its connection and checks the Operator credential again. It then makes a private staging copy with SQLite `VACUUM INTO`, runs `PRAGMA integrity_check`, and syncs the file. It publishes the destination without overwriting anything, and syncs the directory. The destination file appears only when the snapshot is complete and checked.

A backup changes no domain records and has no operation receipt. A second backup to an existing file name returns `conflict`. If a backup is interrupted, check the completed backup or use another file name. If the process crashes during a backup, a private staging file (`.a-stack-backup-*`) can stay in the backup directory. The Operator deletes it.

To make a backup while the service runs:

```sh
backup_dir="$(mktemp -d)"
./a-stack storage backup --destination "$backup_dir/a-stack.sqlite" --json
```

To restore a completed backup, stop the service first. Check the backup, then copy only the backup and the original Operator credential into a new, empty, private directory:

```sh
# backup_dir is the private directory of the backup command.
# Stop the old foreground service with Ctrl-C before you continue.
sqlite3 -readonly "$backup_dir/a-stack.sqlite" 'PRAGMA integrity_check'  # must print ok
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/a-stack"
restore_dir="$(mktemp -d)"
install -m 600 "$backup_dir/a-stack.sqlite" "$restore_dir/state.db"
install -m 600 "$state_dir/operator.json" "$restore_dir/operator.json"
./a-stack serve --state-dir "$restore_dir" --json
```

If the integrity check prints anything but `ok`, do not use that backup; take an older one. Without the `sqlite3` tool, `serve` still runs its own check: it stops with exit code 4 on a damaged copy. The same steps restore a `state.db.pre-vN` copy that `serve` made before a migration.

In another terminal, run `./a-stack health --state-dir "$restore_dir" --json` and make sure that the database ID is the original one. Keep the old state directory until you have checked the restored copy.

Never copy only a live main database file. Never mix a backup with old sidecar files, and never delete sidecar files to try a recovery. Keep the Agent credential files that you gave out in a separate place, because the database hashes cannot rebuild their tokens. See [credential delivery and recovery](lifecycle.md#credential-delivery-and-recovery).

## Internal interfaces

Endpoints in `internal/stack` use these interfaces:

- `IssueCapability(ctx, tx, kind, subjectID, sessionID)` issues a token inside the registration or rebind transaction. The server gets the `Principal` from the stored hash, never from an identity in the request. To revoke or rotate a credential, delete the old hash in the same transaction. The kind and Session scope do not replace the checks of current membership and roles.
- `Store.Mutate(ctx, token, Operation, authorizeReplay, apply)` authenticates again inside its transaction. An `Operation` has a name, a resource scope and a bounded JSON payload. Handlers serialize typed payloads with `json.Marshal`, including the expected revision and all parameters of the change. The digest includes the name and the scope. JSON objects with the same keys in a different order are not guaranteed to match.
- `apply(ctx, tx, principal)` checks that the Session is open, and checks role, membership, references and the expected revision. It then writes the resource, the compact history and any mention references in the same transaction. It returns a `Receipt` with the resource ID, revision, a scalar outcome and an optional body reference ID. A receipt or transaction never contains external calls, body copies or snapshots.
- `authorizeReplay(ctx, tx, principal, receipt)` is required. It checks current read access to the resource of the receipt. It must allow an authorized read after the Session is closed, and it must reject a revoked membership. The service calls it only when the digest matches, before it returns a saved outcome. A digest mismatch returns only `operation_conflict` and does not call the replay callback of the new command. A revoked token fails authentication before the receipt lookup. Browser credentials cannot call `Mutate`.

## Tests

`go test ./...` covers these areas:

- Loopback, authentication and parser limits.
- Permission and FIFO rejection.
- Startup conflicts.
- Migrations in transactions, a private copy before each migration, and rejection of foreign and newer schemas with exit code 4.
- Crash and restart.
- Concurrent retries and current authorization.
- Rollback of failed precommit and receipt writes.
- A real `SQLITE_FULL` error from SQLite page limits, and write failures on read-only storage.
- Safe backup and restore, and the integrity check at start.

`go test -race ./...` runs the same tests with the race detector. Test-only tables exercise the change primitives. Production schemas do not contain them.

The disk-full test uses the real SQLite full-database path without filling the host filesystem. The tests do not simulate a physical device failure or a power cut. Restore tests use real SQLite to check integrity, database identity, credentials, and retained test rows and receipts. These tests make no latency or throughput claim.
