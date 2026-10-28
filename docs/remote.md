# Remote service over Tailscale

An a-stack service can accept HTTPS connections from your tailnet. The service still listens only on loopback. Tailscale Serve terminates HTTPS and forwards requests to it. There is no Funnel or public-internet mode.

A shared Session has one writable copy, on the server. Sharing moves a Session there; linking makes it appear beside your local Sessions. This is not database sync, and there are no offline writes. Existing bearer credentials control access. A Tailscale identity grants access only after an Operator invites that exact login to a Session.

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

`connect` checks reachability, the running version and discovery fields through HTTPS `/v1/health`. It saves the URL, version and database ID, not a credential. Names use letters, digits, underscores and hyphens, up to 64 characters; `list` and `remove` are reserved. An existing name is not overwritten. Remove it before connecting again.

Profiles are private JSON files under the operating system's config directory: `$XDG_CONFIG_HOME/a-stack/remotes` or `~/.config/a-stack/remotes` on Linux, `~/Library/Application Support/a-stack/remotes` on macOS, and `%AppData%\a-stack\remotes` on Windows.

API commands, including `open`, `watch` and `mcp`, accept `--remote NAME`. Supply an explicit credential file issued by that server through a trusted channel. a-stack never substitutes your local Operator credential. `--remote` does not apply to local service management or `new`. The Session picker opens linked Sessions on their server. Transferred and remotely registered Agent credential files remember their remote profile, so their CLI commands and hooks reach that server without an extra flag. Local Agent credentials keep using the local service. Cross-machine Herdr delivery needs a relay; the server does not send notices to a laptop's local runtime endpoint.

Every new remote client fetches the current instance ID. If the database ID changed, it stops before sending the bearer credential. Check the server before removing and recreating the profile. A network failure is an error, not a switch to the local service. Nothing is queued offline. If a write may have committed, retry it with the same operation ID.

## Work together

Start the server with `a-stack serve --share` as above. On your laptop, connect to it and share the open Session of your project folder:

```sh
cd ~/code/my-project
a-stack connect team https://server.example.ts.net
a-stack session share --remote team --credential-file /private/server-operator.json
a-stack session invite friend@example.com
```

The first share needs the server owner's Operator credential, copied through a trusted private channel. Do not give that service-wide credential to a friend. Keep it outside the checkout, in an owner-only directory. With exactly one remote profile, `--remote` can be omitted. The repository must have a network `origin` remote. SSH and HTTPS forms of the same host and repository match; repository path case and non-default ports remain significant.

Your friend signs into Tailscale with the invited login, then runs this in their clone:

```sh
a-stack connect team https://server.example.ts.net
a-stack session link team SESSION_ID
a-stack
```

Use the Session ID printed by share. `session link` also accepts the URL of an already connected profile. The server checks the Tailscale identity and invitation, then accepts a fresh private Session-scoped Operator credential. No server credential needs to be sent to the friend. The link result names its private credential file for explicit remote CLI commands. Both Operators open the same server Session view. They can answer questions, close or reopen that Session, appoint its Orchestrator, and invite another login. They cannot read unrelated Sessions, create unrelated Sessions, revoke all views, or back up the server. They can read the Workspace standing orders but cannot change them. The server owner retains service-wide access.

Run `a-stack` or `a-stack -r` to open a linked Session. If the server is unavailable, opening the link fails; it never opens the old local copy. To bring the Session back to the machine holding its Agent credentials:

```sh
a-stack session unshare
```

This moves the current server data back and seals the server copy. Other machines' links then fail clearly; they do not continue writing to the old server copy.

### Credentials and hooks

Share and unshare need every registered Agent's current private credential. They check the standard `a-stack-agents` paths and recognized installed hooks, not a recursive search of your home directory. For files kept elsewhere, pass `--agent-files /private/agents.json`. This is a JSON object whose keys are Agent IDs and whose values are absolute credential-file paths. Missing or mismatched credentials stop the transfer before cutover.

New credentials are written beside the old files, with the transfer ID in the filename. The old files are not overwritten. Installed hook commands are repointed to the new files and Workspace. Exact native-session enrollment moves only when its machine, credential generation and Agent revision match; the native harness session is retained. Sharing does not enroll an unrelated process just because it runs in the same folder. Keep the original and new files private. Agents with hand-written shell helpers must update those helpers to the new credential paths; installed hooks are updated automatically.

When you register an Agent through `--remote`, its new credential remembers that profile. Its worktree path belongs to the caller's machine, not the server. The Agents view shows the recorded machine. Install and enroll its hooks on the machine running the harness, using its own credential.

### Transfer recovery

The destination imports the snapshot in one transaction, initially read-only. The source stays writable until the destination confirms that import. The source then checks that its read revision still matches the snapshot, seals itself, and the destination activates. During the short sealed/staged gap, requests fail clearly. A transfer never automatically unseals a source whose destination might already be active.

Rerun the same share or unshare command after a network failure or interruption. A private journal under the operating system's config directory, `a-stack/transfers`, retains the transfer ID and prepared credential paths. Do not delete it during a transfer. If the source changed during import, the command leaves it intact and asks you to rerun; the next import replaces the inactive staged copy. A failed import rolls back without changing source work.

Tasks, graph edges, mail and acknowledgements, reports, history, Agent identities and needed standing orders move together. Opaque IDs, revisions and per-Task mail sequence numbers stay. Database-local integer history row keys are reassigned, retaining order and content. Conflicting destination IDs, Agent names or nonempty standing orders stop the import. Browser grants, source capability hashes and local notice-delivery batches are not copied. In-flight notices become uncertain rather than being sent again. Agent retry tombstones move with their Session.

Transfers currently accept snapshots up to 64 MiB and require the same schema on both services. Larger Sessions fail before cutover. Staged data and read-only source recovery rows remain private service data; only the destination is writable.

## Security boundary

- Remote URLs must be HTTPS origins without credentials, a path, a query or a fragment. Certificates are verified normally. Clients ignore proxy environment variables and never follow redirects.
- The service accepts only its loopback Host or its exact configured MagicDNS Host. An Origin must match the URL for that Host. There is no wildcard Host allowance or CORS.
- On the configured Serve Host from a loopback peer, `Tailscale-User-Login` and `Tailscale-User-Name` become request attribution. Serve strips client copies and supplies its own. Missing headers, such as on tagged devices, produce no actor. Non-ASCII names are decoded from Serve's RFC 2047 header encoding. Requests marked by Serve as Funnel traffic are rejected. These headers do not replace bearer authorization, except at the invitation-gated Session enrollment endpoint. Operator actions record the login and display name in history; answers also keep them on the question and its Task mail.
- The configured Serve Host exposes only database ID, instance ID and version at unauthenticated `/v1/health`. Data endpoints retain their bearer and instance checks. Local health remains authenticated.
- Processes on the server can forge proxy headers by calling loopback directly. Do not host untrusted local users there. Tailnet ACLs and HTTPS do not protect the backend from them.

## Operator labels

All Operators remain equal. Labels identify who made a change; they are not accounts or permissions. The service records the trusted login and display name for Needs you answers, standing-order changes, Session close/reopen, Orchestrator appointments, view issue/revoke and Agent credential changes. Agent actions keep their Agent identity, even when sent through Serve.

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
