# Workspaces, Sessions and Agents

This page describes how the Operator sets up a team and how Agents get their credentials. It also covers runtime bindings, and Session close and reopen.

The service gives each Workspace, Session and Agent a stable opaque ID. The CLI always sends these IDs explicitly, from flags, from an Agent credential file that names them, or from the service for an Agent credential (see [Credential context](#credential-context)). The current directory never selects a Workspace or a Session, and lifecycle commands never look up an Agent by its name. There is one exception: the three Operator commands `a-stack`, `a-stack -r` and `a-stack new` use the current folder as a default or a filter, and their output shows the folder they used. No other command and no Agent command uses the current directory. See [Open the last Session](browser.md#open-the-last-session).

## Workspaces, Sessions and Agents

A Workspace is an existing project directory. `workspace create --path ABSOLUTE_DIR` resolves symlinks to get the canonical path, and rejects a path that is already registered. It never uses the current directory. `a-stack new` sends the current folder as this path when no Workspace has exactly that folder, and otherwise reuses that Workspace.

A Session belongs to one Workspace. An Agent registration belongs to one Session for its whole lifetime. To link a separate working directory to the Workspace, use `agent register --worktree ABSOLUTE_DIR`. This does not make a new Workspace.

Limits:

- An Agent name must match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Names are case-sensitive and must be unique in the Workspace.
- A canonical directory path can be 1,024 UTF-8 bytes at most.
- A Session name can be 128 bytes at most.

IDs and private runtime bindings are separate from display names and from Herdr aliases.

## Enroll a team

Start the service as the [README](../README.md) shows. These examples use the default state directory. If you use a different one, give the same `--state-dir ABSOLUTE_DIR` to every command.

```sh
a-stack workspace create --path /absolute/project --operation-id project-create-1 --json
# Set workspace_id to data.resource_id from this receipt.
a-stack session create --workspace "$workspace_id" --name "Build" \
  --operation-id session-create-1 --json
# Set session_id to data.resource_id from this receipt.
credential_dir="$(mktemp -d)"
a-stack agent register --workspace "$workspace_id" --session "$session_id" \
  --name 01-orchestrator --new-credential-file "$credential_dir/orchestrator.json" \
  --operation-id orchestrator-register-1 --json
# Set orchestrator_id to data.resource_id from this receipt.
a-stack session appoint --workspace "$workspace_id" --session "$session_id" \
  --agent "$orchestrator_id" --expected-revision 1 --operation-id appoint-initial-1 --json
```

A new Session has `orchestrator_id: null`. Only the Operator appoints or replaces the Orchestrator. The database allows one Orchestrator per Session, and it must be an Agent of the same Session. An appointment increases the Session revision. If two changes use the same expected revision, one of them gets `conflict` with `current_revision`.

When the Operator replaces the Orchestrator, the old Agent loses the Orchestrator role immediately. It keeps its Agent identity and its ordinary permissions. If the Orchestrator's runtime is absent, idle or done, a-stack does not choose a new Orchestrator, and the other Agents can continue to work.

The Operator or the current Orchestrator can register more Agents in an open Session. This example uses only the Orchestrator's credential:

```sh
a-stack agent register --as agent --credential-file "$credential_dir/orchestrator.json" \
  --workspace "$workspace_id" --session "$session_id" --name 02-worker \
  --new-credential-file "$credential_dir/worker.json" --operation-id worker-register-1 --json
```

Give the new Agent its private `worker.json`, the Workspace, Session and Agent IDs, and the state directory. Keep the Operator credential out of its instructions, its reports and its file references.

These scopes prevent accidental use of the wrong API permissions. They do not protect files from a hostile process: a trusted local process can still read other files of the same operating system account.

An Agent gives its credential in every call. The credential file names its Workspace and Session, so the Agent can leave out `--workspace` and `--session`:

```sh
a-stack session inspect --as agent --credential-file "$credential_dir/worker.json" --json
a-stack agent attach --as agent --credential-file "$credential_dir/worker.json" \
  --agent "$worker_id" --runtime-id explicit-runtime-instance-id --expected-revision 1 \
  --operation-id worker-attach-1 --json
```

Set `worker_id` from the receipt of the Agent's registration. Agent mode and browser mode need an explicit credential file of the matching kind. They never use the Operator file instead. An Agent can read only the Session that it is registered in, also if a request gives another ID. Browser credentials cannot use these lifecycle endpoints. They can read only the Task snapshot and detail endpoints.

## Credential delivery and recovery

`agent register` and the Operator's `agent rebind` need `--new-credential-file`. Its directory must already exist, be private (for example mode 0700) and be outside project checkouts. a-stack rejects a new file in a Git checkout before it makes a token or a file, also when a symlink in a parent directory leads there.

The CLI makes a random token and writes it to the new file with mode 0600. It syncs the file before it sends the request, and it sends only the token's SHA-256 hash. The database and the receipt never contain the token. A private enrollment record in the file links it to the database ID, the operation ID and the exact request digest.

If you lose a response, run the same command again, with the same operation ID, flags, actor credential and new credential file name. The CLI reads the existing file, and the service returns the original receipt. It does not register or rotate again. The file stays in place after a retryable error, also when the outcome is uncertain. When the service refuses the request with an error that is not retryable, such as `forbidden` or `conflict`, the CLI deletes the file that this command wrote, because no Agent holds its token. A file that existed before the command is never deleted.

- A different payload under the same operation ID causes a conflict.
- A credential file that was made for a different request or database also causes a conflict, before anything is sent.
- To correct a rejected request, first find out whether it was committed. Then use a new operation ID and a new file name for the new request.
- Never delete a pending credential file only because a response was lost.

If an Agent loses its token, the Operator recovers access with `agent rebind`. The rebind needs a new operation ID, the current Agent revision and a different new credential file name. The rebind replaces the stored hash, makes all old tokens of that Agent invalid, and changes or clears the runtime binding. The Agent keeps its ID and its history. Keep all credential files that you gave out separately from the database backups, because the hashes cannot rebuild them.

### Credential context

`agent register` and `agent rebind` also write three fields into the new credential file: `workspace_id`, `session_id` and `agent_name`. They are not secret, and they give no access: the service still finds the Agent from the token alone. A rebind looks up the Agent's name with `agent list` before it writes the file.

When a command runs with an Agent credential file that has these fields, the CLI takes `--workspace` and `--session` from the file if they are not given. A flag that is given must name the same ID, or the command fails with `invalid_input` before it sends anything. The context still comes only from a file that the caller names, never from the current folder or from environment variables, which child processes and subagents would inherit.

Credential files from before v0.1.5 have only the kind and the token. For such a file, the CLI asks the service which Workspace and Session the token belongs to (`GET /v1/credential`, which returns `kind`, `workspace_id`, `session_id`, `agent_id` and `agent_name` of the caller's own credential), and fills in `--workspace` and `--session` the same way. The service stores the Session of each token, so the token alone is enough, and the file stays as it is. A service before v0.1.7 has no such read; then the command fails with `invalid_input` and a message that says which flags to add and that the Operator finds the IDs with `a-stack workspace list` and `a-stack session list --workspace WORKSPACE_ID`. Explicit `--workspace` and `--session` keep working with every file. `hook install` still needs them for such a file.

## Runtime bindings

A runtime binding links an Agent registration to the running process that acts as that Agent. Only the Agent's own credential can attach a binding to its new, unbound registration. After that, only the Operator can change the binding, with `agent rebind`:

```sh
a-stack agent rebind --workspace "$workspace_id" --session "$session_id" \
  --agent "$worker_id" --expected-revision 2 --runtime-id replacement-instance \
  --new-credential-file "$credential_dir/worker-replacement.json" \
  --operation-id worker-rebind-1 --json
```

To remove the binding instead, use `--clear-runtime` in place of a new runtime. The Agent is then unattached and has a rotated credential. A rebind works only in an open Session. To recover a credential in a closed Session, reopen the Session first. If a rebind fails with a conflict, a-stack rolls back the credential, the binding claims, the history and the receipt together.

An Agent that runs inside Herdr binds itself to its own pane with `a-stack herdr bind`, in place of `agent attach`. It works for Claude Code, Codex and Pi panes. It takes the same `--agent`, `--expected-revision` and `--operation-id`, reads the pane, the terminal, the native session and the harness version from Herdr and the harness, and sends them as an `agent attach`. For Pi, the native session is the path of its session file. Mention notices reach only Codex for now. See [Enroll a recipient](notifications.md#enroll-a-recipient).

Instead of `--runtime-id`, you can give `--runtime-file PRIVATE_JSON`. The file has `instance_id` and these optional fields: `endpoint`, `pane_id`, `terminal_id`, `native_session_id`, `herdr_alias`, `harness` and `harness_version`. a-stack reads it with the same bounded reader as credential files. An instance ID can be 128 UTF-8 bytes at most, and each optional value 512 bytes at most. List, inspect and receipt output never show the binding. `agent attach` and `agent rebind` run no Herdr command.

The service reserves each concrete target: the instance ID, each endpoint and pane pair, each endpoint and terminal pair, and the native session ID. It treats native session IDs as globally unique. So a new instance ID or alias cannot take over a target that another registration or Session already holds. Only an Operator rebind releases or replaces these reservations, in one transaction.

The target data is only what the caller says. a-stack treats a partial binding or an instance-only binding as unverified, and neither gets automatic notices. The optional [notice adapter](notifications.md) checks the current harness identity before it sends anything. An alias or a pane alone never proves which Agent is there.

### Runtime observations

`agent observe --agent ID --expected-revision N --runtime-id ID --runtime-state unknown|working|idle|done|stopped|blocked` records the latest runtime state. The Operator or the Agent itself can run it. In one transaction, the service checks the context, the binding identity and the Agent revision.

Observations also work when the Session is closed. They do not change the lifecycle revision, the Agent revision, roles or work. Each observation replaces the previous one, and it adds no receipt or history entry. `done` describes the runtime only. It does not finish a Task or delete the Agent. a-stack never releases a target automatically when a process exits or its state is unknown.

## Close, reopen and inspect

Only the Operator can run `session close` and `session reopen`. Both need `--expected-revision` and `--operation-id`. `session inspect` shows the current lifecycle revision, the Orchestrator and whether the Session is closed. Run by an Agent, it also shows that Agent's own ID as `your_agent_id`.

- A retry of the same operation returns its original receipt, also after the Session is closed.
- A new operation with an old revision causes a conflict.
- A new operation for a state that already applies succeeds at the same revision, and it adds no lifecycle event.

A close runs in order with other guarded changes, never at the same time. It keeps all registrations and history, and it never starts or stops terminals. Reopening does not change any work status or runtime.

## Standing orders

Standing orders are the Operator's rules for every Agent of a Workspace, such as "commit as mzmzeee only" or "never change the GitHub About text". They are kept in a-stack, so an Agent on any harness, a subagent, or an agent after a compaction reads the same rules. They override an Agent's own defaults.

```sh
a-stack workspace orders show --workspace "$workspace_id"
a-stack workspace orders set --workspace "$workspace_id" --body-file orders.txt --expected-revision 1 --operation-id orders-0001
a-stack workspace orders clear --workspace "$workspace_id" --expected-revision 2 --operation-id orders-0002
```

- Only the Operator sets and clears them. `--body TEXT` or `--body-file FILE` (`-` for stdin) gives the text: plain UTF-8 of at most 4 KiB, with no control characters other than tabs and line breaks.
- `show` returns `workspace_id`, `body`, `revision` and `updated_at`. A Workspace without orders has an empty `body` and revision 1, so the first `set` takes `--expected-revision 1`. Each change adds one, also a clear. The same text again changes nothing.
- An Agent reads its own Workspace's orders with `workspace orders show`, and in `session inspect` as `standing_orders` (`body`, `revision`, `updated_at`), which is left out while there are none.
- The SessionStart reminder of the [agent hooks](hooks.md) shows the first 8 lines, each cut to 200 characters, and the command that shows the rest. The brief of `a-stack new --orchestrator` shows them the same way, and `task handoff` shows them in full.
- The [browser view](browser.md#what-the-view-shows) shows them read-only. A change gives every Session of the Workspace a new `read_revision`, so open views show it at their next poll.
- Each change adds a `workspace.orders.set` or `workspace.orders.clear` entry to the lifecycle history.

## Lists

`workspace list`, `session list` and `agent list` return `{items, next}`. `next` is present only when more items follow. A list returns 50 items by default. Use `--limit` with a value from 1 to 100. To get the next page, give the `next` value with `--after ID`.

- The Operator can list all Workspaces.
- To list Sessions, the Operator must give a Workspace.
- An Agent must give both IDs to list or inspect, and sees only its own Session.

Metadata never contains bearer tokens, credential hashes or private bindings. Output and errors follow the [command line rules](foundation.md#command-line).

The HTTP endpoints are `POST /v1/workspace/{create,list}`, `/v1/workspace/orders/{show,set,clear}`, `/v1/session/{create,list,inspect,close,reopen,appoint}` and `/v1/agent/{register,list,attach,rebind,observe}`. They take strict typed JSON requests. The CLI builds these payloads. A role or actor name in a request never gives authority.

## Revisions

- The Session `revision` protects lifecycle changes and appointments.
- The Agent `revision` protects the binding identity.
- The Session `read_revision` increases for visible changes to membership, bindings and observations.

Task and graph revisions and the permissions of assignees are described in [Tasks and the Task graph](tasks.md). The general open-Session guard does not give those permissions. History stores stable actor and resource IDs and scalar event, revision and time values. It never stores credentials or binding snapshots.

## Internal guards

Schema v2 upgrades a populated v1 database in one transaction and keeps the database identity, capability hashes and receipts.

Later endpoints use these guards inside `Store.Mutate` callbacks, and then apply their own checks of role, membership, references and revision, and their writes, in the same transaction:

- `ReadSession(ctx, tx, principal, Context)` checks the explicit context and the current registration and scope, also after closure.
- `OpenSession` also rejects closed Sessions. It intentionally does not depend on a runtime or on the presence of an Orchestrator.
- `RequireOrchestrator` gets the current Orchestrator from the Session row.

A digest mismatch returns only `operation_conflict`, before any command-specific replay callback runs. A matching retry finds the resource of the original receipt, and it must pass the current read authorization before it returns the outcome.

The service canonicalizes project and worktree paths before it takes the serialized connection. Only a new, authorized apply uses a canonicalization error. So a committed retry still returns its receipt after the original directory is gone.
