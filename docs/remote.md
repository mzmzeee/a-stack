# Remote service over Tailscale

An a-stack service can accept HTTPS connections from your tailnet. The service still listens only on loopback. Tailscale Serve terminates HTTPS and forwards requests to it. There is no Funnel or public-internet mode.

A shared Session has one writable copy, on the server. Sharing moves a Session there; linking makes it appear beside your local Sessions. This is not database sync, and there are no offline writes. Existing bearer credentials control access. Enrollment needs a one-time code as well as a Tailscale identity. A Session invitation is bound to that exact login and Session.

## On the server

Install Tailscale, sign in, and enable MagicDNS and HTTPS/Serve for the tailnet. The account running a-stack must be allowed to configure Serve. Then run:

```sh
a-stack serve --share
```

The service binds to `127.0.0.1:41677`. It does not choose another port if that port is busy. After it starts, a-stack calls the local `tailscale` CLI and prints the HTTPS URL, such as `https://server.example.ts.net`. The ready JSON also has `shared_url`. `--listen` with `--share` must name that same fixed address.

For a service that starts at login:

```sh
a-stack service install --share
```

The setting is kept in `share.json` in the state directory. It applies on each start, on Linux, macOS and Windows. If the service already runs, the command prints a restart hint; it does not restart that service for you. The restart rechecks the proxy rule, including after a disable-and-enable cycle. A fresh install waits for the shared URL. The service manager's log has startup errors, including missing Tailscale, sign-in, HTTPS permission and port conflicts.

To turn sharing off for a foreground run, use `a-stack serve --share=false`. To turn it off persistently for an installed service, run `a-stack service install` without `--share`. The explicit foreground flag overrides the installed setting only for that run.

Repeated setup is safe. a-stack records its Serve rule in `tailscale-serve.json` in the state directory. It refuses an existing HTTPS port 443 configuration that it did not create, even if the target is the same. Removal checks that the rule is still exactly its own, removes only that HTTPS rule, and leaves other ports alone. It never calls `tailscale serve reset`. If somebody changed the rule or added another handler, removal stops with a conflict instead of deleting their work. Do not edit Serve configuration concurrently with a-stack setup or removal. Keep the ownership file until you have disabled sharing.

Serve runs in the background and keeps its configuration when a-stack stops. No a-stack data is served while the backend is stopped. Disable sharing before removing the service or its state directory.

## On a client

Connect to the URL printed by the server:

```sh
a-stack connect team https://server.example.ts.net
a-stack connect list
a-stack --remote team --as agent --credential-file /private/agent.json task list
a-stack connect remove team
```

`connect` checks reachability, the running version and discovery fields through HTTPS `/v1/health`. Without `--code`, it saves only the URL, version and database ID. With an owner code, it also enrolls a fresh owner credential in the private `a-stack/remote-credentials` config directory. You can retry enrollment with the same name, URL and code after a lost response while the code record remains; the database identity stays pinned. Issuing a new owner code removes expired and used code records. This ends retries with those codes but does not revoke enrolled credentials. Names use letters, digits, underscores and hyphens, up to 64 characters; `list` and `remove` are reserved. An existing name is not overwritten. Remove it before connecting again.

Profiles are private JSON files under the operating system's config directory: `$XDG_CONFIG_HOME/a-stack/remotes` or `~/.config/a-stack/remotes` on Linux, `~/Library/Application Support/a-stack/remotes` on macOS, and `%AppData%\a-stack\remotes` on Windows.

API commands, including `open`, `watch` and `mcp`, accept `--remote NAME`. They use the owner credential enrolled by `connect --code`, or an explicit `--credential-file` issued by that server. a-stack never substitutes your local Operator credential. `--remote` does not apply to local service management or `new`. The Session picker opens linked Sessions on their server. Transferred and remotely registered Agent credential files remember their remote profile, so their CLI commands and hooks reach that server without an extra flag. Local Agent credentials keep using the local service. Remote discovery shares the hook's deadline, so an unreachable server does not hold up the harness. Cross-machine Herdr delivery needs a relay; the server does not send notices to a laptop's local runtime endpoint.

Every new remote client fetches the current instance ID. If the database ID changed, it stops before sending the bearer credential. Check the server before removing and recreating the profile. A network failure is an error, not a switch to the local service. Nothing is queued offline. If a write may have committed, retry it with the same operation ID.

## Work together

Start the server with `a-stack serve --share` as above (or install its shared service). In another terminal **on the server**, run:

```sh
a-stack connect-code
```

It prints a single-use owner code. Send it to your laptop through a private channel. The code expires after 24 hours; it grants service-wide ownership, so do not give it to friends. No credential file needs to be copied. On your laptop, connect and share the open Session of your project folder:

```sh
cd ~/code/my-project
a-stack connect team https://server.example.ts.net --code OWNER_CODE
a-stack session share --remote team
a-stack session invite friend@example.com
```

Use the owner code with `connect --code` and a Session invitation code with `session link --code`; they are not interchangeable. Both commands accept `--code` after their positional arguments.

`session invite` prints a separate single-use code for the friend. Send that code and the Session ID through a private channel. It expires after 24 hours; inviting the same login again replaces its unused code. Codes are stored hashed, never as plaintext in the service. A Session can have up to 16 active (unused, unexpired) invitations, shared by all its Operators. At that limit, a new login returns retryable `rate_limited`; replacing an invitation for the same login still works. Issuing an invitation removes that Session's expired and used invitation rows in the same transaction. A retry with the same already-enrolled credential is allowed until the code expires or is removed by a later invitation; a consumed code cannot enroll a second credential. Cleanup does not revoke enrolled credentials. Exact invitation-operation retries return their original receipt without issuing another code. Successful new invitation operations still add audit history and receipts under the normal retention rules, including replacements for the same login; the active-invitation cap is not a bound on total history or database storage. With exactly one remote profile, `--remote` can be omitted. The repository must have a network `origin` remote. SSH and HTTPS forms of the same host and repository match; repository path case and non-default ports remain significant.

Your friend signs into Tailscale with the invited login, then runs this in their clone:

```sh
a-stack connect team https://server.example.ts.net
a-stack session link team SESSION_ID --code INVITE_CODE
a-stack
```

Use the Session ID printed by share. `session link` also accepts the URL of an already connected profile. The friend needs a running local a-stack. If it is missing, run `a-stack service install` locally, then retry link; the error includes the exact command for a custom state directory. The server checks the Tailscale identity, invitation and code, then accepts a fresh private Session-scoped Operator credential. No server credential needs to be sent to the friend. The link result names its private credential file for explicit remote CLI commands. Both Operators open the same server Session view. They can answer questions, close or reopen that Session, appoint its Orchestrator, and invite another login. They cannot read unrelated Sessions, create unrelated Sessions, revoke all views, or back up the server. They can read the Workspace standing orders but cannot change them. The server owner retains service-wide access.

Run `a-stack` or `a-stack -r` to open a linked Session. The listing checks links concurrently, at most eight at a time, each with its own remote profile and credential and a shared two-second deadline. Closed remote Sessions show as closed. Links that cannot be checked show as `unreachable` (their open/closed state is unknown); JSON rows include `unreachable: true`. Bare `a-stack` only selects Sessions known to be open. Linked Task counts, Needs you counts and latest Task activity are not fetched: the picker shows them as unknown, and JSON uses `null` for `tasks`, `needs_you` and `changed_at`. Local Sessions sort by known activity, followed by links sorted by name and ID; the current folder still takes priority when automatically choosing an open Session. If the server is unavailable, opening the link fails; it never opens the old local copy. To bring the Session back to the machine holding its Agent credentials:

```sh
a-stack session unshare
```

This moves the current server data back and seals the server copy. Other machines' links then fail clearly; they do not continue writing to the old server copy.

### Credentials and hooks

Share and unshare need every registered Agent's current private credential **on the machine running the transfer**, including Agents that ran on other machines. They check the standard `a-stack-agents` paths and recognized installed hooks, not a recursive search of your home directory. For files kept elsewhere, pass `--agent-files /private/agents.json`. This is a JSON object whose keys are Agent IDs and whose values are absolute credential-file paths. Explicit paths are tried first. Agent IDs and names are validated before looking for files. A credential is probed only through its recorded service, after its enrollment database and its profile's resolved URL and database match the source. Different profile names for that same server are allowed; local credentials are never probed against a remote source. If a copied credential's profile is missing on this machine, the error names the `a-stack connect PROFILE URL` command to install it. Verify that server before connecting; do not edit the credential to bypass its recorded service. Missing or mismatched credentials stop the transfer before cutover.

If Agents run on several machines, pause them first. Either keep the Session shared, or bring all their current credential files to the unsharing machine through a private channel and list those paths in the manifest. Do not put credentials in Git. Unshare does not copy remote harnesses or edit their hooks. To resume on the local service, move the harnesses to that machine and explicitly rebind/enroll their native sessions there. Keep the Session shared if they must continue running remotely; old links and credentials will not forward to the local copy.

New credentials are written beside the old files, with the transfer ID in the filename. The old files are not overwritten. Installed hook commands are repointed to the new files and Workspace. Exact native-session enrollment moves only when its machine, credential generation and Agent revision match; the native harness session is retained. Sharing does not enroll an unrelated process just because it runs in the same folder. Keep the original and new files private. Agents with hand-written shell helpers must update those helpers to the new credential paths; installed hooks are updated automatically.

When you register an Agent through `--remote`, its new credential remembers that profile. Its worktree path belongs to the caller's machine, not the server. The Agents view shows the recorded machine. Install and enroll its hooks on the machine running the harness, using its own credential.

### Transfer recovery

The destination imports the snapshot in one transaction, initially read-only. The source stays writable until the destination confirms that import. The source then checks its read revision and retained rows against the snapshot, seals itself, and the destination activates. During the short sealed/staged gap, requests fail clearly. A transfer never automatically unseals a source whose destination might already be active.

Rerun the same share or unshare command after a network failure or interruption. A private journal under the operating system's config directory, `a-stack/transfers`, retains the transfer ID and prepared credential paths. An exclusive folder lock in that same native config directory prevents overlapping local share/unshare commands on Linux, macOS and Windows. Remote profiles and credentials are validated before the first journal is saved. Correct a preflight mistake and retry normally; after export begins, connection arguments and database identities are pinned. Do not delete it during a transfer. If the source changed during import, the command leaves it intact and asks you to rerun; the next import replaces the inactive staged copy. A failed import rolls back without changing source work. Starting a newer export durably fences older source transfers. The source releases a secret proof only after fencing; the destination requires that proof before replacing older staging. A superseded transfer cannot seal the source. Superseding an outbound transfer clears its snapshot in the same transaction, while keeping its ID and release proof. Sealing also clears the outbound snapshot. Inbound rows keep only a small snapshot digest for safe retries, including after activation or supersession. Freed SQLite pages are reused by later writes; clearing payloads does not shrink the database file or run `VACUUM`. This bounds the export rate and retains only one full outbound snapshot, not total storage: transfer IDs and release proofs remain for fencing, and later snapshots include the retained proofs. Finite proof retention needs a separate protocol design.

Tasks, graph edges, mail and acknowledgements, reports, history, Agent identities and needed standing orders move together. Opaque IDs, revisions and per-Task mail sequence numbers stay. Database-local integer history row keys are reassigned, retaining order and content. Conflicting destination IDs, Agent names or nonempty standing orders stop the import. Browser grants, source capability hashes and local notice-delivery batches are not copied. In-flight notices become uncertain rather than being sent again. Agent retry tombstones move with their Session. Sealing or activating a Session refreshes claim-overlap warnings in other Sessions of its Workspace.

Transfers currently accept snapshots up to 64 MiB and require the same schema on both services. Imports require the service owner before the server reads the snapshot. Exports require an Operator and then check access to the requested Session; their request uses the normal API size limit. Each service processes one import or export at a time, returning a retryable `rate_limited` error to others. A Session can prepare at most 16 new exports per UTC clock hour on each service. The budget survives service restarts and is shared by all its Operators. Moving the server clock backwards does not refill it; its stored window must pass before a fresh allowance is available. Further exports return retryable `rate_limited` with a retry delay until the next hour; retries of the current prepared transfer do not consume the budget. Failed exports roll back their budget use. Larger Sessions fail before cutover. Staged data and read-only source recovery rows remain private service data; only the destination is writable. `a-stack new OTHER --orchestrator NEW` skips these recovery copies when checking existing Agents. Their Agent names remain reserved in the local Workspace so a later unshare can restore them without a name conflict; choose a new Orchestrator name.

## Security boundary

- Remote URLs must be HTTPS origins without credentials, a path, a query or a fragment. Certificates are verified normally. Clients ignore proxy environment variables and never follow redirects.
- The service accepts only its loopback Host or its exact configured MagicDNS Host. An Origin must match the URL for that Host. There is no wildcard Host allowance or CORS.
- On the configured Serve Host from a loopback peer, `Tailscale-User-Login` and `Tailscale-User-Name` become request attribution. Serve strips client copies and supplies its own. Missing headers, such as on tagged devices, produce no actor. Non-ASCII names are decoded from Serve's RFC 2047 header encoding. Requests marked by Serve as Funnel traffic are rejected. These headers do not replace bearer authorization, except at enrollment endpoints, which also require a valid one-time code. Operator actions record the login and display name in history; answers also keep them on the question and its Task mail.
- The configured Serve Host exposes only database ID, instance ID and version at unauthenticated `/v1/health`. Data endpoints retain their bearer and instance checks. Local health remains authenticated.
- Processes on the server can forge proxy headers by calling loopback directly, but those headers alone cannot enroll an owner or Session Operator: the one-time code is also required. Keep codes private. Do not host untrusted local users there. Tailnet ACLs and HTTPS do not protect the backend from them.

## Operator labels

Operators with the same scope remain equal. Labels identify who made a change; they are not accounts or permissions. The service records the trusted login and display name for Needs you answers, standing-order changes, Session close/reopen, Orchestrator appointments, view issue/revoke and Agent credential changes. Agent actions keep their Agent identity, even when sent through Serve.

The view shows the answerer's display name, or `Operator (login)` when no name is supplied, and the answer time. `task history`, `task show`, answer mail and mutation receipts include `actor_login` and `actor_name` when available. A retry returns the original receipt and actor. A second answer with a new operation ID names who already answered and when; it never replaces the answer or sends another mail.

Local requests, SSH forwarding, missing identity headers and older history have no actor fields and read as `Operator`. Names are plain text. The login is retained separately from the display name.

## SSH fallback

For an existing local-only service, keep it on a fixed loopback port and forward that same port:

```sh
# On the server:
a-stack serve --listen 127.0.0.1:41677

# On your laptop, if that local port is free:
ssh -N -L 41677:127.0.0.1:41677 server
```

You can open the server's one-time view link through the tunnel. Keep its fragment private. For CLI use, securely copy the server's `service.json` and the appropriate credential into a private client directory outside any checkout, then give that directory with `--state-dir` and the credential with `--credential-file`. Refresh the discovery file after a server restart; its instance ID changes. Do not start a local service on that directory.

`connect` does not accept plain HTTP, including the SSH tunnel. The fallback uses local discovery instead. Both ends must use the same port so the Host check passes. SSH forwarding supplies no Tailscale identity labels.
