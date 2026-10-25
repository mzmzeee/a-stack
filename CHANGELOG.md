# Changelog

All notable changes to a-stack are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and a-stack uses [semantic versioning](https://semver.org/).

The release workflow uses the section of a version as the notes of its GitHub release. Add a section before you push a version tag.

## [Unreleased]

### Added

- Homebrew and Scoop packages. Install with `brew install mzmzeee/tap/a-stack` on macOS and Linux, or `scoop bucket add mzmzeee https://github.com/mzmzeee/scoop-bucket` and `scoop install mzmzeee/a-stack` on Windows. See [Package managers](docs/install.md#package-managers).
- A Working Task with no change and no Task mail for 30 minutes is quiet: its Agent may have stopped. `a-stack status` shows it among the working Tasks, such as `2 working (1 quiet)`, and `--json` has `quiet`. The Task data in `task list`, `task show` and the browser view's pages has `quiet` and `last_mail_at`. `a-stack watch` prints a line and rings the bell once when a Task turns quiet. A Task that waits for an answer to its Needs you question is never quiet. a-stack derives this when it reads a Task, with no schema change and no background job; the view's snapshot ETag counts quiet Tasks, so an open view sees a Task turn quiet in an idle Session. See [Quiet Tasks](docs/watch.md#quiet-tasks).
- `a-stack task handoff --task T` prints a brief that a fresh Agent can start from when another Agent stopped: the Task, its description and report, the open Needs you question, the unfinished prerequisites, the assignees as `@name`, and the latest 20 Task mails (`--limit N`). Plain text by default, `--json` too. It only reads; the mail is there only for an assignee. The skill tells an Agent that takes over a Task, and the Orchestrator that hands one over, to use it. See [Hand a Task over](docs/tasks.md#hand-a-task-over).
- `a-stack storage status` shows `free_in_file_bytes`, the free pages inside `state.db`, and a `note` when they are a quarter of the file or more. After the schema 8 migration the file kept the pages it freed, and the status did not say why the file was bigger; a backup and a restore make it small again.

### Changed

- When the state directory or a new credential file is inside a Git checkout, the error names the folder that has the `.git` and says to choose a folder outside it with `--state-dir`. Before, a user who keeps dotfiles in Git in the home folder got no hint where the checkout was.
- The help of `agent register` says `--name` is the Agent name, and the help of `session create` says it is the Session name, instead of "Session or Agent name".

### Changed

- The README demo GIF is 2.9 MB instead of 7.4 MB, at the same pace, and the demo images show the release version instead of a development build. `scripts/record-demo.sh` drops frames that do not change, keeps fewer frames while the view zooms, uses 64 colors and compresses with gifsicle, which it now needs.

## [0.1.6] - 2026-10-25

Upgrading from v0.1.5: run `a-stack update`, then restart the service (`systemctl --user restart a-stack`, or Ctrl-C and `a-stack serve`). The first start copies the database to `state.db.pre-v7` (`state.db.pre-v6` from v0.1.4), moves it to schema 8 and prints one line that names the copy. It needs free space of up to three times the size of `state.db`; with less, it stops before it changes anything and says how much it needs. After the upgrade, v0.1.5 cannot open the database. To go back, run `a-stack update --version v0.1.5`, stop the service, and put `state.db.pre-v7` back as `state.db` in a new state directory in the same place, as in [Go back to an older version](docs/install.md#go-back-to-an-older-version); changes made after the upgrade are lost. The database file keeps the pages that the migration freed; a backup and a restore make it small again.

### Added

- The agent hooks have a Stop guard and a Task mail nudge, both on by default. The Stop guard blocks a stop once while a Task that this Agent set `working` has no report since. It names the Task and gives the two ways out as whole commands with the Task ID, its revision and operation IDs: report and finish, or set `waiting` with a reason. Other assignees, such as a helper or an Orchestrator that follows the Task, are not blocked, and on a shared Task the reason says to read the report first and, when only your part is done, to say so in Task mail. It never blocks in a closed Session, never twice in a row (also in a harness that does not send `stop_hook_active`, through a marker file), and lets the agent stop on any error. The nudge checks the Agent's Task mail after a tool call or a prompt, at most every 30 seconds per harness session, with its own cursor file in `~/.cache/a-stack/hooks/`, and tells the agent `New Task mail on "TITLE"; run mail check.` without sender or text. The first check of a harness session reads up to the newest message and reports nothing, also when it runs out of time and goes on at the next hook run. The Agent's own cursor file does not move. A check adds about 5 to 13 ms to a tool call, a skipped one under 1 ms. Hooks installed with v0.1.5 get both with the new binary; `hook install --no-stop-guard` and `--no-mail-nudge` turn them off. See [Stop guard](docs/hooks.md#stop-guard) and [Task mail nudge](docs/hooks.md#task-mail-nudge).
- The agent hooks support Pi 0.87 or later. `a-stack hook install --pi` writes the hook list `a-stack-hooks.json` and the extension `extensions/a-stack.ts` into Pi's agent folder (`~/.pi/agent`, or `$PI_CODING_AGENT_DIR`). The extension comes with the binary; it runs the same `a-stack hook` commands on Pi's events, so a Pi agent gets the reminder, the runtime state, the Stop guard and the Task mail nudge. `--pi` is not in the default, has no `--project`, and needs `/bin/sh`. `hook uninstall --pi` removes the extension with the last hooks. Tested end to end with Pi 1.4.0 and its scripted test model. See [Pi](docs/hooks.md#pi).
- `a-stack herdr bind` binds Pi panes. Herdr's Pi integration reports the path of Pi's session file instead of a session ID, and a-stack keeps that path as the native session. Mention notices stay off for Pi until a native fixture verifies them.
- a-stack runs on macOS 13 or later, on Apple silicon and Intel. Releases have `a-stack_darwin_arm64.tar.gz` and `a-stack_darwin_amd64.tar.gz`, built on macOS runners and covered by `SHA256SUMS`, its signature and the attestations. The install script and `a-stack update` pick the archive of your system; under Rosetta, the script still takes the `arm64` archive. The script uses `shasum` when `sha256sum` is missing. macOS has LibreSSL instead of OpenSSL 3, so there the script checks the signature with OpenSSL 3 from Homebrew (`brew install openssl@3`); without it, it checks only the checksum, says so on stderr and links to how to check the signature by hand. On macOS, `a-stack service install` sets up the launchd agent `~/Library/LaunchAgents/dev.a-stack.plist`, which starts at login and again after a crash, with the same three commands and the same refusal while a foreground `a-stack serve` answers; the log is `~/Library/Logs/a-stack.log`. The browser opens with `open`, `a-stack watch --notify` shows notices through `osascript`, and hints that name `systemctl --user` name the `launchctl` command instead. The state directory stays `~/.local/state/a-stack` (or `$XDG_STATE_HOME/a-stack`). The check workflow also builds, vets and tests on macOS. See [Install](docs/install.md) and [On macOS with launchd](docs/install.md#on-macos-with-launchd).
- a-stack runs on Windows 10 or later, on x64 and Arm. Releases have `a-stack_windows_amd64.zip` and `a-stack_windows_arm64.zip`, cross-built on Linux with a pinned llvm-mingw, statically linked against the C runtime, smoke-tested on `windows-latest` and `windows-11-arm` before publishing, and covered by `SHA256SUMS`, its signature and the attestations. `install.ps1` installs with `irm https://github.com/mzmzeee/a-stack/releases/latest/download/install.ps1 | iex` into `%LOCALAPPDATA%\Programs\a-stack`, checks the SHA-256 checksum (not the signature) and adds the folder to the user `PATH`; a Scoop manifest is prepared in `contrib/scoop/`. The state directory is `%LOCALAPPDATA%\a-stack`, created with a protected access list for your account, SYSTEM and Administrators; `service.lock` is held open without sharing. `a-stack service install` sets up the Task Scheduler task `a-stack-<SID>`, which starts at logon and again within a minute after it stops, and the service then writes to `%LOCALAPPDATA%\a-stack\service.log`. `a-stack update` downloads the `.zip`, renames the running `a-stack.exe` to `a-stack.exe.old` (removed at the next start) and refuses Scoop and WinGet folders. The view picker uses the console's VT mode, the browser opens through `ShellExecute`, and the hooks name paths with `/`. Herdr and the desktop notices of `watch --notify` are not available on Windows. The check workflow also builds, vets and tests on Windows and runs `install.ps1` in PowerShell 5.1 and 7. Native Windows tests and smoke checks run in CI before the release is published; see [On Windows](docs/install.md#on-windows) for what still needs a real Windows machine.
- The service logs the cause of each `storage_unavailable` error to stderr, one line per error: the operation, and the Go error type, SQLite result codes or system error number, never a body, an ID, a credential or SQL text. The same line is logged at most once every 10 seconds. Before, a code bug looked like a full disk, and the real cause was lost.
- The browser view's Graph has a semantic zoom: below 50%, cards become blocks in their status color with only the title, at a constant size on screen; below 30%, where a block is too narrow for more than one cut word, blocks without text. Needs you keeps a red ring, and forced colors keep the borders. The blocks keep the cards' size and place, so zooming never runs the layout again.
- The Graph has a minimap at the bottom right: every card as a block in its status color, Needs you ringed, and a box around the part you see. Click or drag on it to move the view. It hides while the graph fits. A Minimap button in the Graph tab's footer, or `m` in the graph, turns it off or on, and the button shows which, also while the graph fits. Screen readers skip the minimap and it never takes the focus.
- Browser view cards and Agent rows show the Task mail count, an envelope and a number, also in their screen reader label. Compact Graph blocks leave it out.
- A browser tab that was open while a-stack was updated says so once the service is back, such as "a-stack was updated to v0.1.7", with a Reload button. The page compares the service's version and its view files (the new `X-A-Stack-Assets` header) with its own. A reload keeps the view's access. Tabs opened from v0.1.5 or older cannot show this.
- A background browser tab now shows Needs you: it polls every 30 seconds, the tab title counts the requests (`(2) Needs you · Session – a-stack`) and the tab icon gets a red dot. An opt-in "Notify me" button shows a system notification when a Task newly needs you; it names the Session and the number of requests, never the question, and sends nothing to the service.
- `A_STACK_REQUIRE_SIGNATURE=1` makes the install script stop and install nothing when it cannot check the release signature: without OpenSSL 3, or for a release before v0.1.5. Without it, the script still installs after the checksum check, and its warning now goes to stderr. See [Install script](docs/install.md#install-script).

- `session inspect` run by an Agent shows that Agent's own ID as `your_agent_id`, so an Agent no longer needs to search `agent list` for itself.
### Changed

- Before a schema migration, `serve` checks that the state directory has room for the copy and the migration, up to three times the size of `state.db` for schema 8, and otherwise stops before it writes anything and says how much space it needs. After a migration, it prints one line that names the `state.db.pre-vN` copy and says that an older a-stack cannot open the database now. See [Update](docs/install.md#update).
- The restore steps restore into the state directory that the CLI uses, after moving the old one aside, instead of into a new folder from `mktemp -d`. In `/tmp`, the restored database could be gone after a reboot, and the CLI, the Agents and the hooks did not find the service there. [Go back to an older version](docs/install.md#go-back-to-an-older-version) has the steps for each service manager.
- `a-stack update` tells you to restart the service only when the service runs the binary that it replaced. `a-stack service status` shows the unit or plist that systemd or launchd loaded, also when it is not the file that `service install` writes, such as `/usr/lib/systemd/user/a-stack.service` from a package. Before, it said "not installed" next to "enabled" and "active".
- `a-stack serve` built without cgo, such as from a `go install` on a Mac without a C compiler, says that it was built without cgo, instead of the general storage error.
- The Linux and macOS release archives name owner 0 and the time of the release commit, hold no extended attributes, and gzip stores no name or time, so the same binary gives the same archive.
- The browser view's Agents tab is a grid of Agent cards. Each header counts the Agent's Tasks in chips ("1 needs you · 3 working · 7 waiting · 1 done"); Needs you and Working Tasks always show as compact rows, Waiting shows its 4 newest (all 5 when there are 5, since a fold would hide one row), and done Tasks fold away. A switch sorts the Agents "Most urgent first" or "By name", and a line on top sums them up. Archived Tasks follow the Archived toggle, which now also shows on the Agents tab. On the 2,000-Task fixture the tab has 39,619 elements instead of 83,823.
- Graph cards out of view skip layout and paint, turned on only after the layout has measured them. On the 2,000-Task fixture, a zoom step above 50% takes 16–20 ms instead of 52–58 ms, below 20% 61–82 ms instead of 92–119 ms, and a pan frame at 33% 16–17 ms instead of 25–29 ms.
- In a short or narrow window, the browser view page scrolls and only the tabs stay at the top, so the view gets the height. At 720×450 (200% zoom), the Graph now gets 324 of 450 pixels instead of 202; on a 375×812 phone, 663 instead of 477.
- The browser view has a System theme button next to Light and Dark, and follows the system theme from the first paint, so a dark system no longer sees a light flash. Mentions in Task mail no longer break inside a name, card shadows are visible, the theme buttons are labelled "Theme", and duplicate CSS rules are gone.
- The browser view's feed keeps the change on its own line, so a long title no longer hides it, and each kind of change has its own icon. The Needs you chooser lists the oldest request first, with the question and its age. A waiting card names up to three blockers and "+N more". A compact block in the Graph shows its full title on hover.
- The Needs you button says "Needs you (2)", or "All clear" in neutral colors. An empty Session suggests to brief an Orchestrator with the a-stack skill, and the Agents tab of a Session with no Agents says "No Agents yet". "Clear filters" shows only when a filter is set.
- The browser view's Task details start with a property grid (Status, Agents, Ticket, Requires, Required by, each dependency with its status icon), hide empty sections, put Task mail last with a "Load 13 more" button, and move the read revision into the footnote.
- In the Graph tab, Tab moves through the cards in the order of the layout, column by column. Before, it followed the random Task IDs.
- The browser view draws only the tab that you see, keeps the dependencies while the snapshot's `graph_revision` stays the same, and indexes Agents and prerequisites once per snapshot. On a 2,000-Task Session, a poll with a change took 2.3–3.9 seconds of main-thread time and now takes 0.2–0.5 seconds; it makes 54 requests and reads 2.4 MB instead of 79 requests and 3.3 MB. Ages tick only where they show, so an idle page no longer lays out every second.
- Browser view cards have a left border in their status color, and a Task that needs you has a red border and ring, so status reads also at Fit on a big Session. When the Graph focuses a Task, a Task that needs you no longer fades, and the other cards fade their background and borders but keep readable text. Before, the fade failed the WCAG AA contrast check.
- A new Task stores no text row for an empty description or for its starting reason `Not started`. Reads show the same texts as before; in the `task.create` history entry, these two texts have no `id`. In a test with 2,000 new Tasks, each took 1,059 bytes instead of 1,817 without a description, and 1,458 instead of 1,835 with a short one.
- Schema 8 stores Task texts and Task mail in rowid tables and drops the extra `(id, task_id)` index on mail, with the same API output. A report or mail body of 1–3 KB no longer takes a 4 KB page of its own. A backup of the 2,000-Task stress fixture is 80.0 MB instead of 91.0 MB (12% smaller); with two 1–3 KB report versions per Task and some long mail, 14.6 MB instead of 18.5 MB (21% smaller). The migration rebuilds those tables once, 2 to 3 seconds for that fixture, and leaves the freed pages in the file for new data; restore a backup to get the smaller file at once.
- The WAL file shrinks back to at most 8 MiB after a burst of writes (`journal_size_limit`). Before, it kept its largest size until the service stopped. The restore steps say that restoring a backup also compacts the database.
- The browser keeps `app.js` and `app.css` and asks the service each time whether they changed (`Cache-Control: no-cache` with an ETag of their content). An unchanged file comes back as an empty 304 answer, which saves 76 KB on each page load. The page itself stays `no-store`.
- The agent skill now asks the Orchestrator to make every piece of work a Task, its own included, and to link each Task to its dependencies when it creates it, then check `prerequisite_count` for missing links. Agents check `task dependencies` before they start, wait with a reason while a prerequisite is open, and ask the Orchestrator for a linked Task when they find new work. The brief from `a-stack new --orchestrator` asks for the links too, and for an Agent for each Task that the Orchestrator hands out instead of one per Task.
- Command help marks `--workspace` and `--session` as "required unless the credential file names it". Before, it said "required", although an Agent whose credential file names them can leave both out.

- A coding agent that you ask to use a-stack, with no brief, now sets itself up: the skill tells it to run `a-stack new --orchestrator 01-orchestrator --no-browser --json` itself and read the IDs from the output, instead of asking you for them. Running that command again returns the same hand-off (`existing_orchestrator: true`) and makes nothing, as long as the credential file belongs to that Session's Orchestrator. The Orchestrator's credential file is now in `a-stack-agents/SESSION_ID/`, so the same name works in every project; before, the second project failed with "already exists". A file in the v0.1.5 place still counts for a repeat. A repeat proves the file through `your_agent_id`, so another Agent's credential file never passes as the Orchestrator's. With `--orchestrator`, `a-stack new` refuses a subfolder of a project that already has a Workspace, so an agent started there does not make a second team. With `--orchestrator`, `a-stack new` never starts the service in the foreground, where it would hang the agent's shell; without a service it says to run `a-stack service install`. With `--orchestrator` and `--no-browser`, it also makes no view link, so no view secret enters the agent's context; the output says to open the view with `a-stack`.
### Fixed

- Orchestrator setup rejects an existing Agent name before creating another Session, and resumes an interrupted appointment only after authenticating the already registered Agent’s credential.

- Orchestrator setup with `--json` never creates a browser view link or launches a browser, even without `--no-browser`.
- Windows release builds stop if DLL inspection fails.

- Windows checks the owner and access list of existing state, credential and backup folders and private files. Service logs stay inside state and are checked before truncation. UNC and device state paths are refused, including resolved network ancestors.
- Windows resolves directory junctions in project, state and update paths. Scoop tasks retain their stable `current` path, and updates recognize custom `SCOOP` and `SCOOP_GLOBAL` roots. Task names include the account SID; executable paths are quoted, and restart hints require waiting for the old task to stop.
- The PowerShell installer verifies that the downloaded executable runs successfully before replacing the installed one. Failed update rollbacks report the saved binary path instead of claiming nothing changed.

- A start that failed after the copy before a schema migration, for example at the foreign key check or on a full disk, wrote another full `state.db.pre-vN` copy on every try, up to 100. Under launchd that was one copy every 10 seconds. A start now uses the copy again when the database has not changed since.
- A crash while `serve` wrote the copy before a migration left a cut-off `state.db.pre-vN`. Opening it rolled it back to an empty database that passed the integrity check, so restoring it gave an empty a-stack. The copy is now written as `state.db.pre-vN.tmp` and renamed when it is complete.
- When `daemon-reload` or `enable` failed, `a-stack service install` left the new unit file behind. It now removes a unit file, plist or task that it just created when the service manager does not take it.
- `a-stack watch --notify-text` passed a question that starts with `-` to `notify-send` as an option.
- The SessionStart reminder of the agent hooks is printed as `hookSpecificOutput.additionalContext` JSON, which Claude Code and Codex read like the plain text before, and which harnesses that parse hook output as JSON no longer drop. Its example command is now a Task command (`… task list`), since `--as agent` does not apply to commands such as `version`.
- `a-stack help hook install` describes `--project` (`.claude` and `.codex`), `--name` (the Agent name) and `--credential-file` (the Agent's file) for the hook commands. `hook install` without `--runtime-id` no longer says that the hooks only print the reminder; they record no runtime state, and the reminder, Stop guard and mail nudge still work.
- An open browser view of a quiet Session now hides a Task when it becomes 24 hours old (the age rule), at its next poll. Before, it showed the Task until something else changed or the page was reloaded. The snapshot ETag now includes the number of Tasks that the age rule archives.
- A second Ctrl-C (or SIGTERM) while `serve` waits up to 10 seconds for open requests ends it at once. Before, the second signal was ignored.
- Before its first snapshot, the browser view said "Nothing needs you" and showed an active Needs you button. It now says that it is loading.
- A browser view that cannot reach the service shows a banner above the view after ten seconds, and "Stale" in the tab title, also when the first load fails. Before, only a small footer line said so, after thirty seconds.
- A browser view whose token the service no longer knows, for example after a restore from an older backup, says within about four seconds of the service answering again that it lost access, why, and to run `a-stack`. The tab title then starts with "No access". Before, it could say for up to 30 seconds that the service had not answered, and the title said "Stale". While the service does not answer at all, the page now tries every four seconds instead of backing off to 30.
- Screen reader labels of browser view cards and rows no longer end a question with "?.", and name three or more Agents as "A, B and C" instead of "A and B and C".
- A card's Needs you label sits next to its progress without a "·", and the Now tab's cards, which are in the Needs you column already, leave it out, so the status line of a card with Task mail no longer wraps.

## [0.1.5] - 2026-10-25

Upgrading from v0.1.4: run `a-stack update`, then restart the service (`systemctl --user restart a-stack`, or Ctrl-C and `a-stack serve`). The first start copies the database to `state.db.pre-v6` and moves it to schema 7. Open browser views lose access once, because old view links are revoked: run `a-stack` to open a new one. If you set up the systemd unit by hand, run `a-stack service install` once to update it.

### Added

- `a-stack new --orchestrator NAME` also registers that Agent, with a credential file in `a-stack-agents` next to the state directory, appoints it as the Orchestrator and prints a brief to paste into the coding agent. It checks the name and the credential file before it makes anything, and refuses a Session that already has an Orchestrator.
- `a-stack status` prints one line per Session: how many Tasks need you, work, wait and finished. `a-stack watch` prints that once, then one line per change (new Tasks, status changes, Needs you, answers, reports) until Ctrl-C, and rings the terminal bell on a new Needs you. `--notify` also shows a desktop notice through `notify-send`, without the question unless you add `--notify-text`. Both use the Operator credential, only read, and show the current folder's Sessions, or `--session ID` or `--all`. Watch reads a Session's Tasks only when its read revision changed. See [Watch from the terminal](docs/watch.md).
- `a-stack statusline` prints a line for Claude Code's status line, such as `a-stack OAuth login: 1 needs you · 2 working`, for the Workspace of the session's folder. It never prints a view URL, and prints nothing on any error. See [Claude Code status line](docs/watch.md#claude-code-status-line).
- `a-stack hook install` adds hooks for one Agent to Claude Code (`settings.json`) and Codex (`hooks.json`), or with `--project` to the current folder. At session start, also after a compaction, the agent gets a reminder of its a-stack name, its open Tasks and to run `mail check`. Prompts and tool calls record the runtime state `working`, a permission request `blocked`, and a stop `idle`, when you give `--runtime-id`. The hook command names the credential file and the IDs explicitly; install takes them from a v0.1.5 credential file when you leave them out. Install keeps all other settings and hooks, and `a-stack hook uninstall` removes only a-stack's hooks for the folder. The hook exits 0 silently on any error and stops itself after 2 seconds, so it never stops the agent. See [Agent hooks](docs/hooks.md).
- The install guide shows `go install github.com/mzmzeee/a-stack/cmd/a-stack@latest`, which needs a C compiler. `contrib/` has an AUR `PKGBUILD` for `a-stack-bin` and a Homebrew formula for Linux, both from the release archives and their checksums, with the steps to publish them. They are not published yet.
- Each Agent has a write budget, so a runaway loop cannot fill the disk. A change costs the size of its request; an Agent can spend 8 MiB at once and 1 MiB more a minute. Past that, a change gets the new error code `rate_limited` (HTTP 429, retryable) with `retry_after_seconds`. A replay of a committed operation ID and the Operator are never limited. In a rerun of the review's reproduction (16 parallel `mail send` loops with 64 KiB bodies from one Agent for 20 seconds), the Agent stored 13,835 messages (925 MB) before and 132 messages (8 MB) now; another Agent could still send.
- `a-stack service install` sets up the systemd user service in one step, so a-stack keeps running after you close the terminal. It writes `~/.config/systemd/user/a-stack.service` (or below `$XDG_CONFIG_HOME`) from the unit in the binary, with the real path of the running binary and the CLI's state directory, then runs `daemon-reload` and `enable --now` and waits until the service answers. It refuses while an `a-stack serve` outside systemd holds the state directory, says when it updated a different unit, and prints the `loginctl enable-linger` command when linger is off. `a-stack service status` shows the unit, enabled, active, binary, linger and health; `a-stack service uninstall` disables the service and removes the unit, and never touches the state directory.
- When `a-stack`, `a-stack -r` or `a-stack new` start the service in the terminal, they say that closing the terminal stops a-stack and how to keep it running.
- `storage status` reports `free_bytes`, and `low_disk` with a `warning` below 1 GiB of free space. The browser view footer then says that disk space is low.
- `a-stack view revoke --workspace ID --session ID` ends browser view access to one Session, and `--all` to every Session. Open tabs say that they lost access at their next refresh. Only the Operator can run it.
- `a-stack herdr bind` lets an Agent that runs inside Herdr bind itself to its own pane, in place of a hand-written runtime file. It reads `HERDR_PANE_ID` and `HERDR_SOCKET_PATH`, asks Herdr for its pane's agent and native session, names an unnamed pane `a-<name>`, reads the harness version from `claude --version` or `codex --version`, and sends the usual `agent attach`. Without Herdr's integration it fails with the new error code `integration_missing` and names `herdr integration install claude` or `codex`. The agent skill has a short step for it.
- `a-stack herdr focus --agent ID` shows a bound Agent's pane in the Operator's Herdr. The service checks the binding against Herdr first, as before a notice, then runs `herdr agent focus`, or `herdr tab focus` if that fails. Only the Operator can run it.
- `contrib/herdr` holds a small Herdr plugin: the action "Open a-stack view" runs `a-stack open` for the Session named in its config file, and the popup pane `watch` runs `a-stack watch` for it. Install it with `herdr plugin install mzmzeee/a-stack/contrib/herdr`.
- `--body TEXT` works on every command that takes one inline text (`task create`, `task edit`, `task status`, `task reopen`, `task report`, `task attention set` and `resolve`), like `--body-file`. The specific flags such as `--report` stay.
- A mistyped command, help topic or flag gets a suggestion, such as `unknown command tsak list (did you mean task list?)`. A stray word gets `unexpected argument abc for task show (did you mean --task abc?)`.
- `a-stack task` and other first words alone print the commands of the group, like `a-stack help task`, and exit with 0. With flags, they fail with `invalid_input` and list the words that can follow.
- Getting started has a section "Hand your Session to an Orchestrator", with the commands the Operator runs after `a-stack new`. The README Quick start points to it.
- `agent register` and `agent rebind` write the Workspace ID, the Session ID and the Agent name into the new credential file. These fields are not secret. With such a file, Agent commands can leave out `--workspace` and `--session`; a flag that is given must match the file. Older credential files keep working with explicit flags.
- `mail check` has a top-level `more`, true when more mail is waiting than the response holds. Without `--cursor-file`, the output then has a `hint`.

### Changed

- Schema 7 adds an expiry to credentials and a table of unused view links. It deletes every existing browser credential, because their secrets are in browser histories. **After the upgrade, open views lose access once: run `a-stack` or `a-stack open` again.**
- The Working status icon turns three times when a Task starts working, then stays still, and waits while the tab is hidden. An endless spin kept the browser busy: about 6.5% of a CPU core with 150 Tasks open.
- `serve` runs SQLite `PRAGMA quick_check` before it says ready. On a damaged database it stops with exit code 4, names the database file and points to the restore steps. On a 940 MB database the check adds about 0.2 seconds to the start.
- A state database with a newer or unknown schema also stops `serve` with exit code 4. The message names the schema version it found and the newest one it supports, and says how to get back: update a-stack, or restore a backup. The systemd unit now has `RestartPreventExitStatus=3 4`, so it does not restart in a loop. Run `a-stack service install` to update the unit.
- Before a schema migration, `serve` writes a private copy of the database, `state.db.pre-vN`, with `VACUUM INTO`. It never replaces an earlier copy, and it does not migrate when it cannot write the copy.
- The restore steps check the backup with `PRAGMA integrity_check` first.
- When the service is not running, cannot be reached or its response is cut off, the CLI error has `retryable: true`, because the change may have been made. The message also says to ask the Operator to start the service, which is what an Agent must do.
- `a-stack update` syncs the new binary to disk before it renames it into place, and syncs the folder after, so a power cut cannot leave an empty binary.
- `a-stack update --version` with an older version warns that the older a-stack may not open the current database, and says how to go back.
- The release notes say to run `a-stack update`.
- `scripts/build-release.sh` pins the versions of the Alpine packages it installs (`gcc`, `musl-dev`, `binutils`).
- Mention notices accept a harness release in the minor line of a fixture-verified version, from that version up: Codex 0.164.x from 0.164.0, where only 0.164.0 was accepted. Herdr is accepted when its running server reports protocol 22 and a compatible CLI and endpoint (`herdr status server --json`), not only when the CLI prints `herdr 0.9.5`. A Herdr that is not running is now `herdr_unavailable`. Claude Code stays mail-only: a partial check with Claude Code 2.1.310 showed bind and focus work, and a mention parks as `unsupported_harness` until a full fixture run. See [Claude Code: partial fixture](docs/notifications.md#claude-code-partial-fixture).
- With Herdr notices on, an Agent without a complete binding (`binding_incomplete`) or with an unsupported harness (`unsupported_harness`) is no longer checked again every 30 seconds. Its notices wait until its binding is attached or changed, or the service starts, and `notifications status` shows `next_attempt_unix_ms: -1`. Each check was a committed write: on the 2,000-Task fixture with 23 such Agents and no clients, the idle service wrote 3,809,280 bytes in 120 seconds before (the WAL grew from 94,792 to 473,832 bytes, 13.7 wakeups a second), and now writes 0 bytes (2.7 wakeups a second).
- `invalid_input` errors name what is wrong: the flag that does not apply to the command, the unknown flag, the bad value (`invalid value "abc" for --expected-revision`), and the missing or bad text field (`--reason is required for --progress waiting`, `title is required`, `reason exceeds 2048 bytes`).
- A bad operation ID gets `operation ID must be 8–128 letters, digits, _ or -`, also on `agent register`, instead of a generic or misleading message. The `--operation-id` help states the rule.
- A mail with a mention that is not a current assignee names each such mention and lists the assignee names.
- An ID flag (`--workspace`, `--session`, `--agent`, `--task`, `--from`, `--to`, `--reference`) with a value that cannot be an ID fails before the CLI contacts the service, for example `--agent takes an Agent ID; 01-orch looks like a name; see a-stack agent list`. The CLI still never looks a name up.
- `a-stack new` opens the open Session of the same name in this folder's Workspace instead of making a second one. Another name still makes a new Session.
- A flag given twice is rejected, except `--mention`. `version` and `--version` reject flags that do not apply to them. `help version` no longer lists credential flags.
- The help lists `session snapshot` and `session page` with the Session commands.
- `mail check --cursor-file FILE` now writes the file: it merges the returned cursors and the Task scan position into it, atomically and with mode 0600, and creates it if it is missing. The same command run again shows only new mail, and the output lists only the Tasks with new mail. Before, a plain `mail check` started at message 1 every time and stopped at 100 messages, so an Agent on a busy Task never saw its newest mail (in the review's reproduction with 105 messages, messages 100–105 never appeared), and every check put old mail back into its context. Now the second check reaches message 105, and a third shows nothing. Two checks with the same file at once leave a whole file; the last write wins. `--cursor-file -` still only reads.
- The agent skill uses `mail check --cursor-file` in its work loop, and its `me` helper needs only the credential file. It no longer reads the Task before every change: it uses the revision of the Task it read or of its last receipt, and reads again only after a `conflict`, so the revision check catches changes by others again. One full work loop takes 8 commands instead of 11. The `rev` and `grev` helper scripts are gone.

### Fixed

- When the service refuses `agent register` or `agent rebind` with an error that is not retryable, the CLI deletes the new credential file that the command wrote. A file that existed before is kept.
- JSON output from the service ends in one newline, not two.
- The docs say which commands take `--state-dir`, and use `a-stack` instead of `./a-stack` in examples.

### Security

- Releases are signed. The release workflow signs `SHA256SUMS` with an Ed25519 key and uploads the raw signature as `SHA256SUMS.sig`. `a-stack update` checks it with the public key in the binary before it trusts the checksums, and the install script checks it with OpenSSL 3 when it is installed. For v0.1.5 and later, a missing or wrong signature stops the update or the install, and nothing changes. Older releases have no signature; they install with a warning, only when you name their version.
- Every release file, `install.sh` included, has a build provenance attestation. The install guide shows how to check a file with `gh attestation verify` and the signature with `openssl`.
- Only the Operator can open the browser view. `open` with an Agent credential fails with `forbidden`. Before, an Agent could open a view and read the Task mail of Tasks it was not assigned to.
- A view link works once, within 60 seconds. The page trades the code in its fragment for a view token, which it keeps for the tab, so a reload works. Opening the same link again shows that it expired. A view token works for 30 days. Each new link deletes the expired links and view tokens. `a-stack`, `a-stack -r`, `a-stack new` and `open` print or open a fresh link as before.

## [0.1.4] - 2026-10-25

### Added

- `a-stack skill install` installs the agent skill in one step. The binary carries the skill of its own version, so it needs no Node.js and no network. It writes `~/.claude/skills/a-stack` (or `$CLAUDE_CONFIG_DIR/skills/a-stack`) and `~/.agents/skills/a-stack`, or with `--project` the same folders in the current folder. It replaces an older copy whole and leaves other skills alone. `a-stack skill uninstall` removes the copies.
- The agent skill docs also describe `npx skills add mzmzeee/a-stack`.
- The install script ends with a hint to run `a-stack skill install`, and says that `a-stack update` updates a-stack next time.
- `a-stack update` installs the latest release in place of the running binary. It finds the latest version from the `/releases/latest` redirect, without the GitHub API, so it skips prereleases. It downloads over HTTPS only, checks the SHA-256 checksum, takes only the `a-stack` file from the archive, checks that the new binary runs and then renames it into place with the same mode. On any failure it changes nothing. `--check` only compares the versions, and `--version vX.Y.Z` installs one release, also an older one. It updates skill copies that `a-stack skill install` wrote, prints how to restart the service, and needs no service and no credential. A build from source updates only with `--version`. `A_STACK_BASE_URL` names a mirror, as for the install script.
- `a-stack skill install --refresh` replaces only the skill copies that exist.

### Changed

- The README demo shows every feature, Task mail included, and holds each step twice as long. Short clips in the browser view guide show the graph, the archive and the Agents tab.

## [0.1.3] - 2026-10-25

### Added

- The browser view has a Now tab, the new default. Its columns show the Tasks that need you (with the question), are working, are waiting (with the prerequisites they wait for) and finished recently, and a feed lists the latest changes. The view remembers the last tab.
- The Task read data has `question_preview`, the start of the open Needs you question, and `last_action`, the action of the Task's latest change. The schema does not change.
- In the Graph tab, selecting a Task highlights it, its direct prerequisites and dependents, and dims the rest. Escape or a click on empty space clears it.
- The Working status icon spins, unless the system asks for reduced motion.
- The Task details in the browser view have a Task mail section. It loads nothing until you open it, then shows the messages oldest first as plain text, with the sender, the age and styled mentions, and "Load more" for long threads. New messages appear on the next poll.
- The Task read data has `mail_count`, and each mail message has `sender_name`.
- `task archive` and `task unarchive` archive a Finished or Cancelled Task, or undo it. The Orchestrator or the Operator runs them; `task reopen` clears the archive. a-stack also counts a done Task as archived when every Task that depends on it is done, or 24 hours after it ended. The Task read data has `archived`, `archived_reason`, `archived_at` and `ended_at`, and the details in the view say why a Task is archived.
- Each Task history entry has `actor_kind`, `agent` or `operator`.

### Changed

- The graph runs left to right with one column per rank, and each Task is centered on its prerequisites, so a chain runs level. Lines are curves from an output port on the right edge of a card. Finished and cancelled Tasks are compact one-line blocks with the Agent names above. Tasks without lines sit below or to the right, whichever Fits larger. The graph draws a dependency line only when no other path connects the two Tasks; the details still list every dependency.
- A selected Task's lines turn the accent color and move on top. Other lines fade to 22% and other cards to 42%.
- Finished and cancelled Tasks show in the graph again, unless they are archived. The graph hides archived Tasks by default and shows them dimmed with the Archived (N) toggle. On the Now board, archived Tasks leave Recently finished and appear under "Show archive".
- The database schema is version 6. It adds `archived_at` and `ended_at` to Tasks and lets the Task history name the Operator. The upgrade runs once, in one transaction.
- The browser view credential can read the Task mail of its own Session with `POST /v1/mail/read`. It still cannot send or check mail. The Operator credential still has no mail access.
- A mail send raises the Session read revision, so a polling view sees the new `mail_count`.
- The service is faster under heavy use. It keeps prepared SQLite statements, `mail check` no longer checks each scanned Task's assignment a second time, and the dependency cycle check searches only the edges it follows. The service process sets the Go garbage collector target to 400 unless `GOGC` is set. With 20 Agents, 2,000 Tasks and 50,000 messages, service CPU per CLI call fell from 5.6-6.3 ms to 1.5-1.7 ms, and the service memory high-water mark rose from 35-37 MB to 48-50 MB. A worst-case `dependency add` took 0.8 ms instead of 70 ms. No contract or schema changes.
- `mail check` checks a saved cursor only when it reads that Task. A malformed cursor for a Task that you are no longer assigned to is ignored, not rejected.
- When the browser view stays stale for thirty seconds but the service still answers, the footer says that the service is busy instead of unreachable.

### Fixed

- Dragging the graph pans it also when the drag starts on a Task card, so a zoomed-in graph can be moved. A click without a drag still opens the Task.

## [0.1.2] - 2026-10-24

The database schema does not change (schema version 5). One change can affect scripts: `serve` now exits with 3, not 1, when another `serve` owns the state directory.

### Added

- `a-stack` without a command opens your last Session in the browser. If no service runs, it starts one in the foreground first.
- `a-stack -r` (or `--resume`) picks a Session in the terminal. Without a terminal, or with `--json`, it prints the Session list.
- `a-stack new [NAME]` creates the Workspace for the current folder if needed, creates a Session and opens it. The CLI generates the operation IDs and prints them.
- `a-stack help COMMAND` and `a-stack COMMAND --help` list the flags of one command. `-h`, `help` and `--version` work.
- `serve` without `--listen` tries the port of its last run first. It keeps the port in `last-port.json` in the state directory. An open browser tab finds the service again after a restart.
- The running version is in the `serve` ready line, in `health` and in the browser view's footer. After an update, compare it with `a-stack version`.
- The Session snapshot has `workspace_name`, the last part of the Workspace path. The view's header shows it.
- An agent skill in `skills/a-stack` teaches a coding agent to work in an a-stack Session. See docs/agent-skill.md.
- Each release includes the systemd user unit `a-stack.service` and the agent skill `a-stack-skill.tar.gz`, and `SHA256SUMS` lists both.
- docs/install.md has an Update section. Release notes come from this file.

### Changed

- `serve` exits with 3 when another `serve` owns the state directory. The error code is still `conflict`. Scripts that check for exit code 1 must check for 3.
- The systemd unit sets `RestartPreventExitStatus=3`. If another `serve` holds the state directory, the unit fails at once and does not restart every 2 seconds.
- The `conflict` error and the errors when no service runs say what to do. The error codes do not change.
- After 30 seconds without the service, the browser view says to run `a-stack open` again if you restarted the service.
- `a-stack --help` lists the commands in groups and fits in 80 columns. `--help --json` keeps its fields and appends `new`.
- The install script says whether it installed, updated or reinstalled a-stack, with the old and the new version. After an update, it tells you how to restart the service.
- The install script stops with an error if the installed binary does not run.
- The release workflow leaves an existing release unchanged when it runs again for the same tag.
- docs/install.md downloads the systemd unit from the release, and docs/agent-skill.md downloads the skill from the release, instead of from the `main` branch.
- The README Quick start uses `a-stack new`, `a-stack` and `a-stack -r`. A new live demo shows a terminal next to the browser view, with new screenshots.

### Fixed

- The browser view remembers the theme you picked after the browser restarts. Until you pick one, it follows your system's light or dark setting.
- Browser view: each group of lines gets its own track in wrapped rows.
- Browser view: the header showed "a-stack" twice. The second label is now the Workspace name.
- Browser view: "1 agent · 1 task" uses the singular.
- The `a-stack -r` picker escapes U+FFFD and U+202E in the names it shows.

## [0.1.1] - 2026-10-24

The database schema does not change (schema version 5).

### Added

- A systemd user unit, `contrib/systemd/a-stack.service`, that starts a-stack automatically and restarts it after a crash. docs/install.md explains how to set it up.
- The MIT license.
- A demo GIF, screenshots and badges in the README.

### Changed

- Browser view: groups of Tasks that do not depend on each other are shown side by side, and tall columns wrap.
- Browser view: Fit fits the Task cards and centers them.
- Getting started is now a list of short, numbered steps.

### Fixed

- Browser view: lines in wrapped columns stay apart, and groups are clearly separate.

## [0.1.0] - 2026-10-24

The first release.

### Added

- Workspaces, Sessions and Agents, with one Orchestrator per Session.
- Tasks and the Task graph: assign Tasks, report progress, ask the Operator questions and add dependencies.
- Task mail with mentions.
- A read-only browser view for the Operator.
- Optional mention notices for Agents that run in Herdr.
- One static Linux binary for `amd64` and `arm64`, with SQLite built in. It accepts connections only on `127.0.0.1`.
- An install script that checks the download against the release checksums.

[Unreleased]: https://github.com/mzmzeee/a-stack/compare/v0.1.6...HEAD
[0.1.6]: https://github.com/mzmzeee/a-stack/compare/v0.1.5...v0.1.6
[0.1.5]: https://github.com/mzmzeee/a-stack/compare/v0.1.4...v0.1.5
[0.1.4]: https://github.com/mzmzeee/a-stack/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/mzmzeee/a-stack/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/mzmzeee/a-stack/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/mzmzeee/a-stack/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mzmzeee/a-stack/releases/tag/v0.1.0
