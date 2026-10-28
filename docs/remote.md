# Remote service over Tailscale

An a-stack service can accept HTTPS connections from your tailnet. The service still listens only on loopback. Tailscale Serve terminates HTTPS and forwards requests to it. There is no Funnel or public-internet mode.

This is the transport foundation. It does not move or link Sessions, enroll remote Operators, or relay Herdr notices to another machine. Existing bearer credentials still control access. A Tailscale identity alone grants no a-stack access.

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

API commands, including `open`, `watch` and `mcp`, accept `--remote NAME`. Supply an explicit credential file issued by that server through a trusted channel. a-stack never substitutes your local Operator credential. `--remote` does not apply to local service management, `new`, the Session picker, or hook installation/events. Hook remote enrollment and per-machine relay support are separate work.

Every new remote client fetches the current instance ID. If the database ID changed, it stops before sending the bearer credential. Check the server before removing and recreating the profile. A network failure is an error, not a switch to the local service. Nothing is queued offline. If a write may have committed, retry it with the same operation ID.

## Security boundary

- Remote URLs must be HTTPS origins without credentials, a path, a query or a fragment. Certificates are verified normally. Clients ignore proxy environment variables and never follow redirects.
- The service accepts only its loopback Host or its exact configured MagicDNS Host. An Origin must match the URL for that Host. There is no wildcard Host allowance or CORS.
- On the configured Serve Host from a loopback peer, `Tailscale-User-Login` and `Tailscale-User-Name` become request attribution. Serve strips client copies and supplies its own. Missing headers, such as on tagged devices, produce no actor. Non-ASCII names are decoded from Serve's RFC 2047 header encoding. Requests marked by Serve as Funnel traffic are rejected. These headers do not replace bearer authorization. Operator actions record the login and display name in history; answers also keep them on the question and its Task mail.
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
