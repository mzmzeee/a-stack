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

Before `serve` opens `state.db`, it takes a nonblocking `flock` on `service.lock`. The lock file stays in place. The service holds the lock until HTTP has stopped and SQLite has closed. A second `serve` on the same directory fails with `conflict`. If the process is killed, the kernel releases the lock, and the next start lets SQLite recover its write-ahead log (WAL). Do not delete the lock file or the SQLite sidecar files. On Windows, the service holds `service.lock` open with no sharing instead of a `flock`, with the same result.

The state directory must belong to the current operating system account and give no access to group or others. The database, discovery and credential files must be private to that account too. a-stack rejects symlinked files and unsafe permissions. It also rejects a state directory inside a Git checkout, also when a symlink in a parent directory leads there. Use a local filesystem. Network filesystems are not supported. Windows has no file modes: there a-stack creates the state directory with a protected access list for your account, SYSTEM and the Administrators group, and rejects symlinks and other reparse points, but it does not check the access list of a directory that exists already.

## Credentials

A credential file holds a bearer token. The database stores only a SHA-256 hash of each token, with the capability kind, the subject ID and the Session scope. Tokens are random 256-bit values. a-stack never prints them.

There are three kinds of credentials:

- The Operator credential manages Workspaces, Sessions and Agents.
- An Agent credential belongs to one registered Agent in one Session.
- A browser credential, or view token, gives read-only access to one Session for 30 days. Only the Operator makes one, through a one-time view link. See [Keep the URL private](browser.md#keep-the-url-private).

On the first start, the service writes `operator.json` privately and durably. Only then does it store the token hash. If the service crashes between these two steps, the next start uses the same file. After that, if `operator.json` is missing, unsafe or does not match, the service does not start. It never makes a replacement. Keep a copy of the original Operator credential with your backups. Only that original file can recover Operator access.

To recover or rotate an Agent credential, the Operator uses `agent rebind`. See [Workspaces, Sessions and Agents](lifecycle.md#credential-delivery-and-recovery).

a-stack trusts the local operating system account. File permissions and bearer checks do not protect against a malicious process that runs as the same account.

## Discovery

`service.json` holds the service's loopback URL, the database ID and an instance ID that changes on each start. The service replaces the file atomically. Clients send the instance ID in the `X-A-Stack-Instance` header. Because of this, an old discovery file cannot send requests to a different service that now uses the same port.

On Ctrl-C, SIGTERM or SIGHUP, the service stops taking new requests and waits up to 10 seconds for open ones; a second signal ends it at once. On a normal shutdown, the service deletes its own discovery file. After an abrupt stop, clients report that the service is unavailable until you start `serve` again.

Without `--listen`, `serve` first tries the port it used the last time it ran without `--listen`. It keeps that port in the private file `last-port.json` in the state directory. If the port is taken, the service picks a free port, records that one, and prints one line on stderr that names both ports. So after a restart, an open browser tab usually finds the service again at the same address. The new instance ID and the bearer checks still apply, and a browser credential still belongs to the database and its Session, not to the port.

Before `serve` says it is ready, it runs SQLite `PRAGMA quick_check` on the state database. If the check fails, `serve` stops with exit code 4 and a message that names the database file and points to [Backup and restore](#backup-and-restore). It never starts on a damaged database. The check reads the whole file: on a 940 MB database it added about 0.2 seconds to the start.

When `serve` is ready, it prints one JSON line on stdout with `status`, `url`, `database_id` and `version`. `health` returns `database_id`, `sqlite_version`, `schema_version` and the `version` of the running service. After an update, compare it with `a-stack version` to see if the service still runs the old binary.

## Command line

Flags can come before or after the command. Each flag can appear once; only `--mention` repeats. All commands accept `--json`. All commands except `version`, `update`, `skill install`, `skill uninstall` and `service uninstall` accept `--state-dir DIR`. `a-stack` without a command, `a-stack -r` and `a-stack new [NAME]` also accept `--no-browser`, and take no credential or `serve` flags. They are described in [Open the last Session](browser.md#open-the-last-session). `serve` also accepts `--listen 127.0.0.1:PORT` (port 0 picks a free port; without the flag, see [Discovery](#discovery)) and `--herdr-executable FILE` (see [Mention notices](notifications.md)). Client commands also accept `--credential-file FILE` and `--as operator|agent|browser`.

`serve` makes or checks its own Operator credential, and it does not accept credential flags. A client without `--credential-file` uses `operator.json` in the state directory. `--as agent` and `--as browser` require `--credential-file`. a-stack rejects a credential file of a different kind, and it never uses the Operator credential instead. An Agent credential file can also give `--workspace` and `--session` (see [Credential context](lifecycle.md#credential-context)).

Workspace, Session, Agent, Task and mail commands need explicit Workspace and Session IDs. `health` and the `storage` commands apply to the whole service:

| Command | HTTP | Credential |
| --- | --- | --- |
| `health` | `GET /v1/health` | Operator or Agent |
| `storage status` | `GET /v1/storage` | Operator |
| `storage backup --destination ABSOLUTE_FILE` | `POST /v1/storage/backup` with `destination` | Operator |

`a-stack --help` (also `-h` or `help`) lists the commands in groups. `a-stack help COMMAND` or `a-stack COMMAND --help` lists the flags of one command and marks the required ones. `--workspace` and `--session` are required unless the credential file names them (see [Credential context](lifecycle.md#credential-context)). The flag list comes from the same table that rejects flags a command does not accept. For a first word such as `task`, the help lists the commands that start with it. `a-stack task` alone prints the same list and exits with 0. With flags, a first word alone is an `invalid_input` error that lists the words that can follow it. `--version` works like `version`. With `--json`, help prints the same `{"data":{"commands":[...],"flags":[...]}}` list for every command.

Every command except `open`, `update`, `skill install`, `skill uninstall`, `service install`, `service uninstall`, `service status`, `status`, `watch`, `statusline`, `hook`, `hook install`, `hook uninstall`, the three Operator commands and `--help` prints a JSON `data` object on stdout, also without `--json`. Those commands print plain lines without `--json`. If a command fails, it prints a JSON `error` object on stderr and nothing on stdout. An `invalid_input` message names what is wrong: the unknown or misplaced flag, the bad value, the extra word or the missing text. When a command or flag is close to what you typed, it asks, for example `unknown command tsak list (did you mean task list?)` or `unexpected argument abc for task show (did you mean --task abc?)`. The ID flags `--workspace`, `--session`, `--agent`, `--task`, `--from`, `--to` and `--reference` take IDs only. The CLI rejects any other value before it contacts the service, for example `--agent takes an Agent ID; 01-orch looks like a name; see a-stack agent list`. It never looks a name up. The exit code is 0 for success, 2 when the error code is `invalid_input`, and 1 for all other errors. Two exceptions, for `serve` and the Operator commands that start it:

- Exit code 3: another `serve` already owns the state directory and does not answer them. The error code is still `conflict`.
- Exit code 4: the state database cannot be used as it is. It failed the integrity check, or it has a newer or unknown schema (see [SQLite](#sqlite)). The error code is `storage_unavailable` with `retryable: false`. A restart cannot help, so the systemd unit does not restart after exit codes 3 and 4.

The CLI never retries a request automatically. Client commands stop normally on SIGINT and SIGTERM, also while they wait for stdin. Only the foreground service catches these signals, so that it can shut down cleanly. The `a-stack -r` picker also catches them, restores the terminal and then ends the same way. If an interrupted change was possibly committed, send it again with the same operation ID and payload to get the original receipt.

## HTTP rules

The service listens only on `127.0.0.1`. Every data request needs the instance ID and a bearer token, except `POST /v1/browser/redeem`, which trades the one-time code of a view link for a view token. The `Host` header must match the bound address. If a request has an `Origin` header, it must match the service URL. The service has no cross-origin access, and it rejects query parameters, so a token can never be in a query string. Clients do not follow redirects and do not use proxy settings from the environment.

A browser credential can use only the Session read endpoints and `POST /v1/mail/read` for the Tasks of its Session. It cannot use the `health` or `storage` endpoints. An Agent credential can use `health`, but it cannot read storage status or make backups.

Request rules:

- A JSON body can be 1 MiB at most. It must be UTF-8 and contain exactly one value. Unknown fields are rejected.
- A `GET` request with a body is rejected, also with chunked encoding.
- Private JSON files, such as credentials, can be 4 KiB at most. A file that is not a regular file cannot block a credential read.
- Header size and the read, write and idle timeouts have limits.

An error response is a JSON `error` object with `code`, `message` and `retryable`. It also has `current_revision` when that applies, and `retry_after_seconds` for `rate_limited`. A Task conflict has the current revision and `current_progress`. A second Operator question on the same Task also has `current_question`. Error messages never repeat the input, credentials, paths or SQL. Only the start errors of `serve` with exit code 4 name the state database file, on the Operator's own terminal. Standard request logging is off, so the service keeps no log that can grow without limit.

## Write budget

A misbehaving Agent could otherwise fill the disk, because a-stack never deletes retained data. So each Agent has a write budget. Each change an Agent commits costs the size of its JSON request, which holds every text the change stores: mail bodies, reports, descriptions, questions and so on. An Agent can spend 8 MiB at once, and the budget refills by 1 MiB a minute. When the budget cannot pay for a change, the service refuses it with `rate_limited` (HTTP 429). The error is retryable and has `retry_after_seconds`, which the HTTP answer also sends as `Retry-After`. Wait that long and send the same command again with the same operation ID.

- Heavy use stays below the budget. In the [stress test](performance.md), each of 20 Agents makes about 25 calls a second with small bodies, which costs at most about 450 KiB a minute.
- The burst lets an Orchestrator set up thousands of Tasks at once, and it is larger than the largest request (1 MiB), so every valid change fits.
- A runaway loop stores 8 MiB and then at most 1 MiB a minute, about 1.4 GiB a day per Agent.
- A retry of a committed operation ID returns its receipt and is never refused. A refused or failed change costs nothing.
- The Operator has no budget. The budget is kept in memory, per Agent, and starts full when the service starts.

The numbers are constants in `internal/stack/limits.go`.

## SQLite

The service uses one serialized `database/sql` connection with WAL, `synchronous=FULL`, foreign keys and a busy timeout of 5,000 ms. Transactions use `BEGIN IMMEDIATE`. Each schema migration commits together with its `user_version` and `application_id` updates. The service rejects a newer, unsupported or foreign schema and does not reset it. SQLite does its own checkpoints and sidecar cleanup. The WAL file is about 4 MB in normal use. After a burst of writes, such as a migration, it is cut back to 8 MiB (`journal_size_limit`) when SQLite starts it again after a checkpoint, instead of keeping its largest size until the service stops.

A database with a newer schema comes from a newer a-stack, for example after an update to an older version. `serve` then stops with exit code 4. The message names the schema version it found and the newest one it supports. To get back, update a-stack to the newer version with `a-stack update`, or restore a backup made with this version.

Before a migration, `serve` copies the database to `state.db.pre-vN` in the state directory, where N is the old schema version. It uses SQLite `VACUUM INTO`, the file is private (0600), and it is synced to disk. It never replaces an earlier copy: the next copy with the same version is `state.db.pre-vN.2`, and so on. If the copy cannot be written, for example because the disk is full, `serve` stops and does not migrate. The copy is a normal backup: to go back to the older a-stack, restore it as in [Backup and restore](#backup-and-restore). Changes made after the migration are not in it. a-stack never deletes these copies; delete them yourself when you no longer need them.

Each schema version added one area:

1. Database metadata, hashed capabilities and compact operation receipts.
2. [Workspaces, Sessions and Agents](lifecycle.md).
3. [Tasks, the Task graph and bounded snapshots](tasks.md).
4. [Task mail and mention references](mail.md).
5. [Notice batches and bounded retry diagnostics](notifications.md).
6. [Archived Tasks and Operator entries in the Task history](tasks.md#archived-tasks).
7. [One-time view links and expiring view tokens](browser.md#keep-the-url-private). It deletes the older browser credentials.
8. Task texts and [Task mail](mail.md#storage-for-notices) in rowid tables, which store long reports and mail bodies more compactly, and without the extra `(id, task_id)` index on mail. Schema 8 rebuilds these tables, which SQLite allows only with foreign keys off; the migration turns them off for its transaction and runs `PRAGMA foreign_key_check` before the commit. A database with a broken reference is not migrated, and `serve` stops with exit code 4.

A migration that rebuilds tables leaves the old pages free inside the database file, so the file does not shrink at once: new Tasks and mail use the free pages before the file grows again. A backup is compact, so to get the smaller file at once, make a backup and [restore it](#backup-and-restore). With the 2,000-Task fixture of the [stress test](performance.md), the migration from schema 7 added 2 to 3 seconds to the start (3.4 to 4.2 seconds instead of 1.2 to 1.5 seconds in three runs); the file grew from 91.0 MB to 113.1 MB with 32.0 MB free, and a backup of it is 80.0 MB.

A storage error returns `storage_unavailable`. The answer never says why, but the service writes one line to stderr (the journal under systemd) for each such error, for example `a-stack: storage_unavailable in task.status: SQLite error 13 (database or disk is full), extended code 13`. The line names the operation or Go function, and the Go error type, the SQLite result codes or the system error number. It never contains a body, an ID, a credential or SQL text. The same line is written at most once every 10 seconds, followed by the number of times it was held back, so a retry loop cannot fill the journal. A failed commit never returns a successful receipt. If the connection drops during a commit, the outcome is unknown, so the caller must retry with the same operation ID. The CLI marks this case `retryable: true`: when the service is not running, cannot be reached, or the response was cut off.

`storage status` reports the current sizes of the main database, WAL and SHM files, and `free_bytes`, the free space of the file system that holds them. These sizes can change between two calls. The free space is measured at most 10 seconds earlier. Below 1 GiB, the status also has `low_disk: true` and a `warning`, and the [browser view](browser.md#refresh) footer shows a warning. When the free space cannot be measured, `free_bytes` is absent. a-stack never deletes retained data. The single connection and the backup operation run work one at a time. Add read concurrency only after a measurement shows the need.

## Operation IDs and receipts

Commands that change records take an `--operation-id` of 8 to 128 ASCII letters, digits, underscores or hyphens. Any other ID gets `invalid_input` with the message `operation ID must be 8–128 letters, digits, _ or -`. (`open` makes its own operation ID. `agent observe`, `storage backup` and `view revoke` take none.) The service stores a compact receipt for each change, under the actor's kind and subject and the operation ID.

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
a-stack storage backup --destination "$backup_dir/a-stack.sqlite" --json
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
a-stack serve --state-dir "$restore_dir" --json
```

A backup is compact: SQLite writes it without the free pages of the database file. The database never shrinks while it runs, because a-stack never deletes retained data and SQLite keeps freed pages for reuse. So restoring a backup is also the way to compact the database, for example after a migration that rebuilt tables.

If the integrity check prints anything but `ok`, do not use that backup; take an older one. Without the `sqlite3` tool, `serve` still runs its own check: it stops with exit code 4 on a damaged copy. The same steps restore a `state.db.pre-vN` copy that `serve` made before a migration.

In another terminal, run `a-stack health --state-dir "$restore_dir" --json` and make sure that the database ID is the original one. Keep the old state directory until you have checked the restored copy.

Never copy only a live main database file. Never mix a backup with old sidecar files, and never delete sidecar files to try a recovery. Keep the Agent credential files that you gave out in a separate place, because the database hashes cannot rebuild their tokens. See [credential delivery and recovery](lifecycle.md#credential-delivery-and-recovery).

## Internal interfaces

Endpoints in `internal/stack` use these interfaces:

- The service writes the Operator credential file at the first start. The CLI makes Agent credentials: `agent register` and `agent rebind` send only the hash of the new token, and the service stores it in the registration or rebind transaction. The service makes a view token when a view link is redeemed (`issueViewToken`). The server gets the `Principal` from the stored hash, never from an identity in the request. To revoke or rotate a credential, delete the old hash in the same transaction. The kind and Session scope do not replace the checks of current membership and roles.
- `Store.Mutate(ctx, token, Operation, authorizeReplay, apply)` authenticates again inside its transaction. An `Operation` has a name, a resource scope and a bounded JSON payload. Handlers serialize typed payloads with `json.Marshal`, including the expected revision and all parameters of the change. The digest includes the name and the scope. JSON objects with the same keys in a different order are not guaranteed to match.
- `apply(ctx, tx, principal)` checks that the Session is open, and checks role, membership, references and the expected revision. It then writes the resource, the compact history and any mention references in the same transaction. It returns a `Receipt` with the resource ID, revision, a scalar outcome and an optional body reference ID. A receipt or transaction never contains external calls, body copies or snapshots.
- `authorizeReplay(ctx, tx, principal, receipt)` is required. It checks current read access to the resource of the receipt. It must allow an authorized read after the Session is closed, and it must reject a revoked membership. The service calls it only when the digest matches, before it returns a saved outcome. A digest mismatch returns only `operation_conflict` and does not call the replay callback of the new command. A revoked token fails authentication before the receipt lookup. Browser credentials cannot call `Mutate`. For an Agent, `Mutate` checks the [write budget](#write-budget) after the replay lookup and charges it after the commit.

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
- Safe backup and restore.
- The write budget with a test clock, a mail burst from one Agent next to another Agent, and replay past the budget.
- The low-disk flag in `storage status` and on the snapshot answer, with a fake free-space source.

`go test -race ./...` runs the same tests with the race detector. Test-only tables exercise the change primitives. Production schemas do not contain them.

The disk-full test uses the real SQLite full-database path without filling the host filesystem. The tests do not simulate a physical device failure or a power cut. Restore tests use real SQLite to check integrity, database identity, credentials, and retained test rows and receipts. These tests make no latency or throughput claim.
