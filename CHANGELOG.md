# Changelog

All notable changes to a-stack are listed here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and a-stack uses [semantic versioning](https://semver.org/).

The release workflow uses the section of a version as the notes of its GitHub release. Add a section before you push a version tag.

## [Unreleased]

### Added

- Each Agent has a write budget, so a runaway loop cannot fill the disk. A change costs the size of its request; an Agent can spend 8 MiB at once and 1 MiB more a minute. Past that, a change gets the new error code `rate_limited` (HTTP 429, retryable) with `retry_after_seconds`. A replay of a committed operation ID and the Operator are never limited. In a rerun of the review's reproduction (16 parallel `mail send` loops with 64 KiB bodies from one Agent for 20 seconds), the Agent stored 13,835 messages (925 MB) before and 132 messages (8 MB) now; another Agent could still send.
- `a-stack service install` sets up the systemd user service in one step, so a-stack keeps running after you close the terminal. It writes `~/.config/systemd/user/a-stack.service` (or below `$XDG_CONFIG_HOME`) from the unit in the binary, with the real path of the running binary and the CLI's state directory, then runs `daemon-reload` and `enable --now` and waits until the service answers. It refuses while an `a-stack serve` outside systemd holds the state directory, says when it updated a different unit, and prints the `loginctl enable-linger` command when linger is off. `a-stack service status` shows the unit, enabled, active, binary, linger and health; `a-stack service uninstall` disables the service and removes the unit, and never touches the state directory.
- When `a-stack`, `a-stack -r` or `a-stack new` start the service in the terminal, they say that closing the terminal stops a-stack and how to keep it running.
- `storage status` reports `free_bytes`, and `low_disk` with a `warning` below 1 GiB of free space. The browser view footer then says that disk space is low.
- `a-stack view revoke --workspace ID --session ID` ends browser view access to one Session, and `--all` to every Session. Open tabs say that they lost access at their next refresh. Only the Operator can run it.
- `--body TEXT` works on every command that takes one inline text (`task create`, `task edit`, `task status`, `task reopen`, `task report`, `task attention set` and `resolve`), like `--body-file`. The specific flags such as `--report` stay.
- A mistyped command, help topic or flag gets a suggestion, such as `unknown command tsak list (did you mean task list?)`. A stray word gets `unexpected argument abc for task show (did you mean --task abc?)`.
- `a-stack task` and other first words alone print the commands of the group, like `a-stack help task`, and exit with 0. With flags, they fail with `invalid_input` and list the words that can follow.
- Getting started has a section "Hand your Session to an Orchestrator", with the commands the Operator runs after `a-stack new`. The README Quick start points to it.

### Changed

- Schema 7 adds an expiry to credentials and a table of unused view links. It deletes every existing browser credential, because their secrets are in browser histories. **After the upgrade, open views lose access once: run `a-stack` or `a-stack open` again.**
- The Working status icon turns three times when a Task starts working, then stays still, and waits while the tab is hidden. An endless spin kept the browser busy: about 6.5% of a CPU core with 150 Tasks open.
- `serve` runs SQLite `PRAGMA quick_check` before it says ready. On a damaged database it stops with exit code 4, names the database file and points to the restore steps. On a 940 MB database the check adds about 0.2 seconds to the start.
- A state database with a newer or unknown schema also stops `serve` with exit code 4. The message names the schema version it found and the newest one it supports, and says how to get back: update a-stack, or restore a backup. The systemd unit now has `RestartPreventExitStatus=3 4`, so it does not restart in a loop. Download the unit again to get this.
- Before a schema migration, `serve` writes a private copy of the database, `state.db.pre-vN`, with `VACUUM INTO`. It never replaces an earlier copy, and it does not migrate when it cannot write the copy.
- The restore steps check the backup with `PRAGMA integrity_check` first.
- When the service is not running, cannot be reached or its response is cut off, the CLI error has `retryable: true`, because the change may have been made. The message also says to ask the Operator to start the service, which is what an Agent must do.
- `a-stack update` syncs the new binary to disk before it renames it into place, and syncs the folder after, so a power cut cannot leave an empty binary.
- `a-stack update --version` with an older version warns that the older a-stack may not open the current database, and says how to go back.
- The release notes say to run `a-stack update`.
- `scripts/build-release.sh` pins the versions of the Alpine packages it installs (`gcc`, `musl-dev`, `binutils`).
- `invalid_input` errors name what is wrong: the flag that does not apply to the command, the unknown flag, the bad value (`invalid value "abc" for --expected-revision`), and the missing or bad text field (`--reason is required for --progress waiting`, `title is required`, `reason exceeds 2048 bytes`).
- A bad operation ID gets `operation ID must be 8–128 letters, digits, _ or -`, also on `agent register`, instead of a generic or misleading message. The `--operation-id` help states the rule.
- A mail with a mention that is not a current assignee names each such mention and lists the assignee names.
- An ID flag (`--workspace`, `--session`, `--agent`, `--task`, `--from`, `--to`, `--reference`) with a value that cannot be an ID fails before the CLI contacts the service, for example `--agent takes an Agent ID; 01-orch looks like a name; see a-stack agent list`. The CLI still never looks a name up.
- `a-stack new` opens the open Session of the same name in this folder's Workspace instead of making a second one. Another name still makes a new Session.
- A flag given twice is rejected, except `--mention`. `version` and `--version` reject flags that do not apply to them. `help version` no longer lists credential flags.
- The help lists `session snapshot` and `session page` with the Session commands.

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

[Unreleased]: https://github.com/mzmzeee/a-stack/compare/v0.1.4...HEAD
[0.1.4]: https://github.com/mzmzeee/a-stack/compare/v0.1.3...v0.1.4
[0.1.3]: https://github.com/mzmzeee/a-stack/compare/v0.1.2...v0.1.3
[0.1.2]: https://github.com/mzmzeee/a-stack/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/mzmzeee/a-stack/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/mzmzeee/a-stack/releases/tag/v0.1.0
