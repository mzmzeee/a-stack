# Install

a-stack runs on Linux, macOS and Windows, on x86-64 (`amd64`) and ARM64 (`arm64`). The Linux release binaries are statically linked. They do not need other libraries. The macOS release binaries run on macOS 13 or later and use only the libraries that come with macOS. The Windows release binaries run on Windows 10 or later and use only the libraries that come with Windows.

On Linux and macOS, a-stack keeps its data in `~/.local/state/a-stack`, or in `$XDG_STATE_HOME/a-stack` when `XDG_STATE_HOME` is set. macOS uses the same folder as Linux, so the commands in these guides and the agent skill work the same on both. On Windows, the folder is `%LOCALAPPDATA%\a-stack`; see [On Windows](#on-windows).

## Install script

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
```

The script does these steps:

1. It finds the archive for your processor. On a Mac with Apple silicon it takes the `arm64` archive, also when the shell runs under Rosetta.
2. It downloads the archive and the release's `SHA256SUMS` file.
3. It checks the signature of `SHA256SUMS` with OpenSSL 3, if OpenSSL 3 is installed. macOS has LibreSSL instead, so there the script uses OpenSSL 3 from Homebrew (`brew install openssl@3`). Without OpenSSL 3, it checks only the checksum and says so on stderr. See [How releases are verified](#how-releases-are-verified).
4. It compares the checksum. If the signature or the checksum is not correct, it stops and installs nothing.
5. It installs the binary in `~/.local/bin`, without `sudo`.
6. It tells you if this folder is not on your `PATH`.

To change the version or the folder, or to refuse an install without a checked signature, set these variables:

| Variable | Default | Example |
| --- | --- | --- |
| `A_STACK_VERSION` | the latest release | `v0.1.0` |
| `A_STACK_INSTALL_DIR` | `~/.local/bin` | `$HOME/bin` |
| `A_STACK_REQUIRE_SIGNATURE` | not set: without OpenSSL 3, check only the checksum | `1`: stop and install nothing when the signature cannot be checked, also for a release before v0.1.5 |

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | A_STACK_VERSION=v0.1.0 A_STACK_INSTALL_DIR="$HOME/bin" sh
```

To read the script before you run it, download it first:

```sh
curl -fsSL -o install.sh https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh
less install.sh
sh install.sh
```

### Install script for Windows

In PowerShell (Windows PowerShell 5.1 or PowerShell 7), run:

```powershell
irm https://github.com/mzmzeee/a-stack/releases/latest/download/install.ps1 | iex
```

The script does these steps:

1. It finds the archive for your processor, `a-stack_windows_amd64.zip` or `a-stack_windows_arm64.zip`.
2. It downloads the archive and the release's `SHA256SUMS` file.
3. It compares the SHA-256 checksum. If it is not correct, it stops and installs nothing. It does not check the signature of `SHA256SUMS`, because PowerShell cannot check an Ed25519 signature; to check it, see [How releases are verified](#how-releases-are-verified). `a-stack update` checks the signature of every later update.
4. It runs the downloaded executable’s `version` command and checks its exit status before replacing any existing binary. It installs `a-stack.exe` in `%LOCALAPPDATA%\Programs\a-stack`, without administrator rights. If a-stack runs from there, the old binary moves aside to `a-stack.exe.old`, which the next start of a-stack removes.
5. It adds this folder to your user `PATH`. Open a new terminal to use it.

If Windows or the installer stops between moving the old executable aside and placing the new one, restore `a-stack.exe.old` to `a-stack.exe`, or run the installer again. A running older copy may instead have a numbered `.old` name beside it.

The script takes the same variables as `install.sh`, `A_STACK_VERSION` and `A_STACK_INSTALL_DIR`, and also `A_STACK_NO_PATH=1`, which leaves your `PATH` as it is:

```powershell
$env:A_STACK_VERSION = 'v0.1.6'; irm https://github.com/mzmzeee/a-stack/releases/latest/download/install.ps1 | iex
```

To read the script before you run it, download it with `irm https://github.com/mzmzeee/a-stack/releases/latest/download/install.ps1 -OutFile install.ps1`, read it, then run `powershell -ExecutionPolicy Bypass -File install.ps1`.

## Manual download

1. Open the [releases page](https://github.com/mzmzeee/a-stack/releases).
2. Download the archive for your system, `SHA256SUMS` and `SHA256SUMS.sig`. The archives are `a-stack_linux_amd64.tar.gz` and `a-stack_linux_arm64.tar.gz` for Linux, `a-stack_darwin_arm64.tar.gz` (Apple silicon) and `a-stack_darwin_amd64.tar.gz` (Intel) for macOS, and `a-stack_windows_amd64.zip` and `a-stack_windows_arm64.zip` for Windows.
3. Check the archive, either with the release signature or with the GitHub attestation. See [How releases are verified](#how-releases-are-verified). Then check the checksum:

   ```sh
   sha256sum --check --ignore-missing SHA256SUMS
   ```

   On macOS, use `shasum -a 256 --check --ignore-missing SHA256SUMS`. On Windows, in PowerShell, compare `(Get-FileHash a-stack_windows_amd64.zip).Hash` with the line of the archive in `SHA256SUMS`; the case of the letters does not matter.

4. Unpack the binary into a folder on your `PATH`:

   ```sh
   tar -xzf a-stack_linux_amd64.tar.gz -C ~/.local/bin a-stack
   ```

   On Windows, unpack `a-stack.exe` from the zip into a folder on your `PATH`, such as `%LOCALAPPDATA%\Programs\a-stack`, with `Expand-Archive a-stack_windows_amd64.zip -DestinationPath $env:LOCALAPPDATA\Programs\a-stack`.

## Go install

With Go installed, you can build and install a release with one command:

```sh
CGO_ENABLED=1 go install github.com/mzmzeee/a-stack/cmd/a-stack@latest
```

- It needs a C compiler, such as `gcc`, on macOS the Xcode command line tools (`xcode-select --install`), or on Windows a MinGW-w64 `gcc` on the `PATH`, because SQLite is built in with cgo. Without one, the build fails. Without `CGO_ENABLED=1` and without a C compiler, Go builds without cgo, and `a-stack serve` stops and says that it was built without cgo.
- It needs Go 1.27.3 or later. An older `go` downloads that version itself, unless `GOTOOLCHAIN=local` is set.
- It installs the binary in `$(go env GOBIN)`, or `~/go/bin` when `GOBIN` is not set. Make sure that folder is on your `PATH`.
- The build takes about a minute. `a-stack version` shows the release, such as `v0.1.4`. Use `@vX.Y.Z` instead of `@latest` for another version.
- The binary uses your system's C library, like a [build from source](#build-from-source). It does not check the release signature; Go checks the module against the Go checksum database instead.

`a-stack update` replaces this binary with the release binary, in the same folder.

## Package managers

- **Homebrew**, on macOS 13 or later and Linux: `brew install mzmzeee/tap/a-stack`. Update with `brew upgrade`. The service runs the binary by its path in the Homebrew Cellar, which changes with each version, so run `a-stack service install` again after an upgrade.
- **Scoop**, on Windows: `scoop bucket add mzmzeee https://github.com/mzmzeee/scoop-bucket`, then `scoop install mzmzeee/a-stack`. Update with `scoop update a-stack`. Scoop cannot replace a running program, so stop the service first; see [On Windows with Task Scheduler](#on-windows-with-task-scheduler).

Both install the release binary and check it against the release's `SHA256SUMS`. An AUR package (`a-stack-bin`) is prepared in [`contrib/`](../contrib/README.md) but not published yet. If you install a-stack with a package manager, update it with that package manager: `a-stack update` cannot replace a binary in `/usr/bin`, and on Windows it refuses to replace a binary in a Scoop or WinGet folder and names `scoop update a-stack` or `winget upgrade a-stack` instead.

## How releases are verified

The release workflow on GitHub Actions builds every release. It signs the `SHA256SUMS` file with an Ed25519 key that only the workflow can use, and uploads the signature as `SHA256SUMS.sig`. The file has the raw 64-byte signature. The public key is in `install.sh` and in the `a-stack` binary:

```text
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEANkJiNRRt3iLovFX+EVrAxA1Z7a3diAw6F3PhyC9+D08=
-----END PUBLIC KEY-----
```

- `a-stack update` checks the signature before it trusts `SHA256SUMS`. A missing or wrong signature stops the update, and nothing changes.
- The install script checks it with OpenSSL 3 or later: the `openssl` on your `PATH`, or else the one from Homebrew (`openssl@3` below `$HOMEBREW_PREFIX`, `/opt/homebrew` or `/usr/local`). macOS has LibreSSL, not OpenSSL 3, so on a Mac run `brew install openssl@3` first. Without OpenSSL 3, the script says on stderr that it did not check the signature, links to this section and checks only the checksum. With `A_STACK_REQUIRE_SIGNATURE=1`, it stops instead and installs nothing.
- The checksum alone comes from the same server as the archive, so it shows only that the download is complete. The install script does not run the downloaded binary to check its own signature: that binary is not trusted yet, and a changed one could say that it is fine. Once a-stack is installed, `a-stack update` checks every later release with the key in the installed binary.
- The Windows install script, `install.ps1`, checks only the checksum. To check the signature on Windows, use `openssl` from Git for Windows (`C:\Program Files\Git\usr\bin\openssl.exe`, OpenSSL 3) as shown below.
- Releases up to v0.1.4 have no signature. `a-stack update` and `install.sh` accept them with a warning, but only when you name such a version: `a-stack update --version v0.1.4` or `A_STACK_VERSION=v0.1.4`. For v0.1.5 and later, a missing signature is an error.

To check a signature by hand, save the public key above as `a-stack-release.pem` and run:

```sh
openssl pkeyutl -verify -pubin -inkey a-stack-release.pem -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig
```

It prints `Signature Verified Successfully`. On macOS, run `"$(brew --prefix openssl@3)/bin/openssl"` instead of `openssl`. The macOS and Linux archives name owner 0 and the time of the release commit and hold no extended attributes, so the same binary always gives the same archive. Then check the archive with `sha256sum --check --ignore-missing SHA256SUMS`, or on macOS with `shasum -a 256 --check --ignore-missing SHA256SUMS`.

Releases also have GitHub build provenance attestations: since v0.1.5 for every file, `install.sh` included, and before that for the archives. With the [GitHub CLI](https://cli.github.com/), check a downloaded file like this:

```sh
gh attestation verify a-stack_linux_amd64.tar.gz --repo mzmzeee/a-stack
```

It shows that the file was built by the a-stack release workflow from the tagged commit.

## Check the installation

```sh
a-stack version
```

The output shows the version that you installed:

```json
{"data":{"version":"vX.Y.Z"}}
```

## Update

Your data stays in the state directory when you update. The service needs a restart before it uses the new version.

1. Optional: back up your data first. The service must be running for this:

   ```sh
   mkdir -p -m 700 ~/a-stack-backups
   a-stack storage backup --destination ~/a-stack-backups/before-update-$(date +%F-%H%M%S).db
   ```

2. Update the binary:

   ```sh
   a-stack update
   ```

   It downloads the latest release and the release's `SHA256SUMS` and `SHA256SUMS.sig` files over HTTPS. It checks the [signature](#how-releases-are-verified) and the checksum. If one of them is not correct, it stops and changes nothing. It then checks that the new binary runs and replaces the `a-stack` binary that you ran, in one step, with the same file mode. It syncs the new file and its folder to disk, so a power cut cannot leave an empty binary. It prints the old and the new version, for example `Updated a-stack v0.1.3 -> v0.1.4 in /home/you/.local/bin/a-stack`, or `a-stack v0.1.4 is already the latest version`. It needs no service and no credential, and it never uses `sudo`.

   In a terminal, it shows the steps on stderr, and a bar while it downloads the archive: the percent, the megabytes done and in total, and the speed, such as `Downloading [##########----------]  52%  9.7 / 18.6 MB  2.2 MB/s`. When the server does not send the size, it shows the megabytes without a percent. The bar redraws its line with a carriage return, without colors or other escape codes. With `NO_COLOR` or `TERM=dumb`, it shows the steps without the bar. When stderr is not a terminal, or with `--json`, it prints neither, and the output is the same as before.

   If you installed the agent skill with `a-stack skill install`, the update also updates those copies, in your home folder and in the current folder. It does not create new ones.

   - `a-stack update --check` only compares the versions. When a newer release exists, it prints `a-stack v0.1.4 is available; you have v0.1.3. Run a-stack update to install it.` Otherwise it says that your version is the latest. It exits with 0 in both cases. With `--json`, `update_available` is `true` or `false`.
   - `a-stack update --version v0.1.2` installs that release, also an older one. For an older version it prints a warning: an older a-stack may not open the current database. See [Go back to an older version](#go-back-to-an-older-version).
   - A build from source has no release version. It updates only with `--version`.
   - If you cannot write to the binary's folder, the command stops and names the folder. Install a-stack in a folder that you own with the install script and `A_STACK_INSTALL_DIR`.
   - On Windows, it downloads the `.zip` archive. Windows cannot replace a running `a-stack.exe`, so the update renames it to `a-stack.exe.old` and puts the new binary in its place; the next start of a-stack removes the old file. If the binary is in a Scoop or WinGet folder, the update stops and names `scoop update a-stack` or `winget upgrade a-stack`.
   - `A_STACK_BASE_URL` names a release mirror, as for the install script.

   Releases up to v0.1.3 do not have `a-stack update`. To update them, or as the other way, run the install script again:

   ```sh
   curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
   ```

   To pick a version with the script, set `A_STACK_VERSION` as in [Install script](#install-script).

3. Restart the service. Until then, the running service keeps the old version. `a-stack update` prints the restart command when the service runs the binary that it replaced.

   - If you started `a-stack serve` by hand, press Ctrl-C in its terminal and run `a-stack serve` again.
   - If systemd runs the service, run `systemctl --user restart a-stack`.
   - If launchd runs the service on macOS, run `launchctl kickstart -k gui/$(id -u)/dev.a-stack`.
   - Replace `a-stack-SID` below with the task name from `a-stack service status`. If Task Scheduler runs the service on Windows, run `schtasks /End /TN a-stack-SID` and, once Task Scheduler shows it has stopped, `schtasks /Run /TN a-stack-SID`.

4. If the open browser tab does not find the service again, open the view again. This command opens your last Session:

   ```sh
   a-stack
   ```

During the restart, Agent commands fail with `storage_unavailable` and `retryable: true` for a few seconds. An Agent can run the same command again with the same operation ID. a-stack makes the change only one time.

When the new version has a new database schema, the first start migrates the database. Before that, it writes a private copy of the database, `state.db.pre-vN`, in the state directory (N is the old schema version). After the migration, `serve` writes one line to its output (the journal under systemd, the log file under launchd and Task Scheduler). It names the copy, says that an older a-stack cannot open the database now, and links to the next section.

Schema 10 adds Task file claims and expired-operation records. From v0.1.7 it copies to `state.db.pre-v9`; from v0.1.6 to `state.db.pre-v8`; from v0.1.5 to `state.db.pre-v7`. Allow free space of three times `state.db` from v0.1.7 or v0.1.6, or four times from v0.1.5. The existing free-space check and private copy run before any migration.

The migration needs free space in the state directory. For schema 8 (v0.1.6) and schema 9 (after v0.1.6), plan for up to three times the size of `state.db`: one copy, and twice the size of the tables that it rebuilds (Task texts and mail, or only mail for schema 9), first in the WAL file and then in the database file. From v0.1.5 straight to schema 9, both rebuilds run in one step: plan for up to four times the size of `state.db`. If less space is free, `serve` stops before it writes anything and says how much it needs. If a start fails after the copy, the next start of the unchanged database uses that copy again instead of writing another one. After the migration, the database file keeps the freed pages; to make it small again, make a backup and restore it (see [Backup and restore](foundation.md#backup-and-restore)).

### Go back to an older version

An older a-stack cannot open a database that a newer one migrated. `a-stack serve` then stops with exit code 4 and a message that names the schema version it found and the newest one it supports. The systemd unit does not restart it. launchd on macOS tries again every 10 seconds, and Task Scheduler on Windows every minute; each try writes the same message to the log. To get back:

- Update to the newer version again: `a-stack update`, or `a-stack update --version vX.Y.Z`.
- Or, to stay on the older version, put back the copy that the migration made, `state.db.pre-vN`, or a backup made with the older version. Changes made after that copy are lost, and Agents registered after it must be registered again.

To put the copy back, install the older version first with `a-stack update --version vX.Y.Z`. Then stop the service:

- If you started `a-stack serve` by hand, press Ctrl-C in its terminal.
- If systemd runs the service, run `systemctl --user stop a-stack`.
- If launchd runs the service on macOS, run `launchctl bootout gui/$(id -u)/dev.a-stack`. launchd keeps starting an older a-stack that stops with exit code 4, so `kill TERM` is not enough.
- If Task Scheduler runs the service on Windows, run `schtasks /Change /TN a-stack-SID /DISABLE` and then `schtasks /End /TN a-stack-SID`.

Move the state directory aside, and put the copy and the Operator credential in a new state directory in the same place. For rollback from schema 10, use `state.db.pre-v9` for v0.1.7, `state.db.pre-v8` for v0.1.6 or `state.db.pre-v7` for v0.1.5. Use the exact name that `serve` printed after the migration, which may have a numbered suffix. This example restores v0.1.7:

```sh
state_dir="${XDG_STATE_HOME:-$HOME/.local/state}/a-stack"
old_dir="$state_dir.before-restore-$(date +%Y%m%d-%H%M%S)"
mv "$state_dir" "$old_dir"
mkdir -m 700 "$state_dir"
install -m 600 "$old_dir/state.db.pre-v9" "$state_dir/state.db"
install -m 600 "$old_dir/operator.json" "$state_dir/operator.json"
```

On Windows, the state directory is `%LOCALAPPDATA%\a-stack`; move it aside and copy the two files in the same way.

Start the service again: run `a-stack serve`, or `systemctl --user start a-stack`, or `launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/dev.a-stack.plist`, or `schtasks /Change /TN a-stack-SID /ENABLE` and then `schtasks /Run /TN a-stack-SID`. The CLI, the Agents and the agent hooks find the service as before, because the state directory is the same. Keep the old folder until you have checked the restored database, as in [Backup and restore](foundation.md#backup-and-restore).

Then delete the saved mail cursor files: each Agent's `mail check --cursor-file` file, and the agent hooks' cursor folder, `~/.cache/a-stack/hooks` (below `$XDG_CACHE_HOME` when set; `~/Library/Caches/a-stack/hooks` on macOS; `%LOCALAPPDATA%\a-stack-hooks` on Windows). A cursor file saved after the upgrade can point past the restored mail, and new mail that gets the same numbers would then be skipped. Without its cursor file, an Agent's next `mail check` starts again at the first message, and the hooks start again at the newest one.

## Start a-stack automatically

If you start a-stack with `a-stack`, `a-stack new` or `a-stack -r`, the service runs in that terminal. When you close the terminal, a-stack stops. Instead, a user service can run `a-stack serve` for you. It starts when you log in and starts again after a crash. You do not need `sudo` or administrator rights.

Run this command:

```sh
a-stack service install
```

It uses systemd on Linux, launchd on macOS and Task Scheduler on Windows. First, it checks that no `a-stack serve` runs for the state directory outside the service manager. If one runs, it stops and changes nothing. Press Ctrl-C in the terminal of that service, then run the command again. Only one service can use a state directory.

The service runs the `a-stack` binary that you ran, with its real path, and the state directory that the CLI uses. Run `a-stack service install` again after you move the binary or change the state directory. It changes nothing when the service is up to date.

`a-stack service status` shows the service, the binary and the state directory that it uses, and the result of `a-stack health`. `--json` prints the same as one JSON object. `a-stack service uninstall` stops the service and removes it. It never touches the state directory. The sections below give the details for each system.

### On Linux with systemd

On Linux, `a-stack service install` sets up a systemd user service. systemd starts it when your user manager starts, and starts it again if it crashes. It does these steps:

1. It writes the unit `~/.config/systemd/user/a-stack.service` (or below `$XDG_CONFIG_HOME`). If the unit exists and differs, it replaces the unit and says `Updated`. If systemd runs the service already, it tells you to restart the service to use the new unit.
2. It runs `systemctl --user daemon-reload` and `systemctl --user enable --now a-stack`. If one of them fails and the unit file is new, it removes the unit file again, so the failed install leaves nothing behind.
3. It waits until the service answers, and prints its URL. If the service does not answer, read the log with `journalctl --user -u a-stack`.
4. It checks linger. Without linger, systemd starts your user services only when you log in, and stops them when you log out. If linger is off, the command prints the command that turns it on, and does not run it:

   ```sh
   loginctl enable-linger "$USER"
   ```

`a-stack service status` shows whether the unit file exists, whether it is enabled and active, the binary and the state directory in the unit, and linger. When systemd loads an `a-stack` unit from another file, such as `/usr/lib/systemd/user/a-stack.service` from a package, status shows that file and its binary and state directory, and `--json` has it as `loaded_unit_file`.

`a-stack service uninstall` stops and disables the service, removes the unit file and runs `systemctl --user daemon-reload`.

To read the service's log, run `journalctl --user -u a-stack`. At each start, the service writes one ready line with its URL.

To stop the service until the next boot or login, run `systemctl --user stop a-stack`. systemd starts the service again only when it fails, so it stays stopped. To stop it and not start it automatically again, run `systemctl --user disable --now a-stack`. To remove the unit, see [Uninstall](#uninstall).

### On macOS with launchd

On macOS, `a-stack service install` sets up a launchd agent. launchd starts it when you log in, and starts it again after a crash. The agent runs only while you are logged in; macOS has nothing like linger. It does these steps:

1. It writes the agent `~/Library/LaunchAgents/dev.a-stack.plist`. It writes the service's output, such as the ready line with the URL, to `~/Library/Logs/a-stack.log`. If the plist exists and differs, it replaces the plist and says `Updated`. If launchd runs the agent already, it prints the two commands that load the new plist.
2. If the agent is not loaded, it runs `launchctl enable gui/UID/dev.a-stack` and `launchctl bootstrap gui/UID ~/Library/LaunchAgents/dev.a-stack.plist`, with your user ID as UID. If the agent is loaded, it runs `launchctl kickstart gui/UID/dev.a-stack`, which starts the service only if it stopped. If one of these commands fails and the plist is new, it removes the plist again.
3. It waits until the service answers, and prints its URL. If the service does not answer, read the log and the output of `launchctl print gui/$(id -u)/dev.a-stack`.

The plist names the state directory with `--state-dir`, because launchd does not get the variables of your shell.

launchd starts the service again after it exits with an error. Unlike systemd, it cannot skip exit codes 3 (another `a-stack serve` owns the state directory) and 4 (the database is damaged or has a newer schema), so it tries again every 10 seconds, and the log shows the error each time. Stop the other `a-stack serve`, or see [Go back to an older version](#go-back-to-an-older-version).

`a-stack service status` shows the plist, launchd's state of the agent (such as `running`, or `not loaded`), the binary and the state directory in the plist, and the log file. With `--json`, `unit` is the label `dev.a-stack`, `unit_file` is the plist, `loaded` and `active` take the place of `enabled` and `linger`, and `log` is the log file. When launchd loaded the agent from another plist, status shows that plist, as `loaded_unit_file` with `--json`.

`a-stack service uninstall` runs `launchctl bootout gui/UID/dev.a-stack` and removes the plist. It also unloads an agent whose plist is already gone. It never touches the log.

To control the service by hand:

```sh
launchctl kickstart -k gui/$(id -u)/dev.a-stack   # restart
launchctl kill TERM gui/$(id -u)/dev.a-stack      # stop until the next login
launchctl kickstart gui/$(id -u)/dev.a-stack      # start again
```

After `kill TERM`, the service exits with 0, so launchd does not start it again until you log in again or run `kickstart`.

### On Windows with Task Scheduler

On Windows, `a-stack service install` sets up a Task Scheduler task named `a-stack-SID`, where `SID` is your Windows account SID. Task Scheduler starts it when you log in, and starts it again within a minute after it stops. The task runs only while you are logged in, with your normal rights, also on battery, and without a time limit. It does these steps:

1. It creates the task with `schtasks /Create`. If the task exists and differs, it replaces the task and says `Updated`. If the service runs already, it prints the commands that restart it.
2. If the service does not run, it starts the task with `schtasks /Run /TN a-stack-SID`. If that fails and the task is new, it deletes the task again.
3. It waits until the service answers, and prints its URL. If the service does not answer, read the log and the output of `schtasks /Query /TN a-stack-SID /V /FO LIST`.

The service started by Task Scheduler has no terminal. It leaves its console window and writes its output, such as the ready line with the URL, to `service.log` inside the private state directory: `%LOCALAPPDATA%\a-stack\service.log` for the default state directory. At each start, it empties the log when the log is larger than 1 MiB. A console window can show for a moment when the task starts.

Each Windows account has its own task name; no shared `a-stack` task is overwritten or removed. Scoop installations keep the stable `current` path in the task so an upgrade does not leave it pointing to a removed version.

The task names the state directory with `--state-dir`.

Task Scheduler cannot skip exit codes 3 (another `a-stack serve` owns the state directory) and 4 (the database is damaged or has a newer schema), so it tries again every minute, and the log shows the error each time. Stop the other `a-stack serve`, or see [Go back to an older version](#go-back-to-an-older-version).

`a-stack service status` shows the task, whether it is enabled, the binary and the state directory in the task, and the log file. With `--json`, `unit` is the task name and `installed` and `enabled` say whether the task exists and is enabled.

`a-stack service uninstall` stops the task and deletes it. It never touches the log.

To control the service by hand, replace `a-stack-SID` with the task name printed by `a-stack service status`. The commands work in PowerShell and cmd.exe. For a restart, end the task, wait until Task Scheduler shows it has stopped, then run it:

```text
schtasks /End /TN a-stack-SID
schtasks /Run /TN a-stack-SID
```

To stop it and keep it stopped:

```text
schtasks /Change /TN a-stack-SID /DISABLE
schtasks /End /TN a-stack-SID
```

To start it again:

```text
schtasks /Change /TN a-stack-SID /ENABLE
schtasks /Run /TN a-stack-SID
```

`schtasks /End` alone stops the service only until the task starts it again, within a minute.

### Set up the systemd unit by hand

Releases up to v0.1.4 do not have `a-stack service`. On Linux, you can also set up the unit by hand.

The unit runs `~/.local/bin/a-stack`. If you installed the binary in another folder, change the path in the `ExecStart` line.

1. Stop any `a-stack serve` that you started by hand. A second service stops with `conflict`. The unit then shows as failed with exit code 3 and does not restart. It also does not restart after exit code 4, when the database is damaged or has a newer schema.

   Press Ctrl-C in the terminal where `a-stack serve` runs. If you closed that terminal, run this command:

   ```sh
   pkill -INT -f 'a-stack serve'
   ```

   `a-stack health` then fails, because no service runs.

2. Download the unit into your systemd user folder:

   ```sh
   mkdir -p ~/.config/systemd/user
   curl -fsSL -o ~/.config/systemd/user/a-stack.service https://github.com/mzmzeee/a-stack/releases/latest/download/a-stack.service
   ```

   If you installed a specific version, use `https://github.com/mzmzeee/a-stack/releases/download/vX.Y.Z/a-stack.service` instead. Releases up to v0.1.1 do not include the unit. For those, or if you have a checkout of the repository, copy the unit from the checkout:

   ```sh
   cp contrib/systemd/a-stack.service ~/.config/systemd/user/
   ```

3. Load the unit, then enable and start it:

   ```sh
   systemctl --user daemon-reload
   systemctl --user enable --now a-stack
   ```

4. Check that the service runs:

   ```sh
   systemctl --user status a-stack
   a-stack health
   ```

   `systemctl --user status a-stack` must show `active (running)`. If it shows `activating (auto-restart)` or `failed`, read the log with `journalctl --user -u a-stack`. A `conflict` error in the log means that another `a-stack serve` still runs. Go back to step 1.

   The service needs a moment to write its discovery file. If `a-stack health` fails right after the start, wait a second and run it again.

5. Let the service start at boot, before you log in:

   ```sh
   loginctl enable-linger "$USER"
   ```

#### Use the same state directory as the CLI

`a-stack service install` writes the CLI's state directory into the unit, so you need this only for a unit that you set up by hand.

The unit uses the default state directory, `~/.local/state/a-stack`. It sets this folder with `--state-dir` and does not read `XDG_STATE_HOME`. The systemd user manager can have a different environment than your shell. At boot, it usually does not have the variables that your desktop session sets.

The CLI uses `$XDG_STATE_HOME/a-stack` when `XDG_STATE_HOME` is set. Run `echo "$XDG_STATE_HOME"` in your shell. If it is empty or `~/.local/state`, you do not need to change anything. If it is another folder, add a drop-in that gives the service the same folder:

```sh
mkdir -p ~/.config/systemd/user/a-stack.service.d
cat > ~/.config/systemd/user/a-stack.service.d/state-dir.conf <<EOF
[Service]
ExecStart=
ExecStart=%h/.local/bin/a-stack serve --state-dir "$XDG_STATE_HOME/a-stack"
EOF
systemctl --user daemon-reload
systemctl --user restart a-stack
```

The empty `ExecStart=` line clears the command from the unit. The quotes keep a path with spaces in one piece. systemd reads `%` as the start of a specifier, such as `%h` for your home folder. If your path contains `%`, open `state-dir.conf` and write each `%` in the path as `%%`. You can add other `serve` flags in the same way, such as `--herdr-executable` (see [Mention notices](notifications.md)).

## On Windows

a-stack is built for Windows 10 or later, on x64 and Arm. These things differ from Linux and macOS:

- The state directory is `%LOCALAPPDATA%\a-stack`, or `%XDG_STATE_HOME%\a-stack` when `XDG_STATE_HOME` is set. `a-stack serve` creates state, and `a-stack new --orchestrator` creates its credential folder, with an access list that only your account, SYSTEM and the Administrators group can use, and that does not take the rights of the parent folder. Files inside get the same access. Existing state, credential and backup folders keep their access lists, but a-stack refuses owners or access grants outside your account, SYSTEM and Administrators. Credential folders for `agent register` must exist first. Windows checks access lists instead of Unix file modes. UNC and device state paths are refused, including a state path whose existing ancestor resolves to a network share.
- A second `a-stack serve` for the same state directory stops with `conflict`, as on Linux: the running service keeps `service.lock` open, and Windows does not let another process open it.
- The service runs from Task Scheduler; see [On Windows with Task Scheduler](#on-windows-with-task-scheduler).
- `a-stack` opens the view in your default browser.
- The hooks name paths with `/`, such as `C:/Users/you/...`, so that Git Bash, which Claude Code uses for hooks on Windows, can run them.
- Herdr does not run on Windows, so the `herdr` commands and `--herdr-executable` stop with an error that says so. Agents get Task mail with `mail check`.
- `a-stack watch --notify` shows no desktop notices on Windows yet; it says so and works without them.

Windows support is new. CI is configured to run the tests on Windows x64 and start the release binaries on Windows x64 and Arm, but these things were not yet tried on a real Windows machine: the access list of a state directory that you name with `--state-dir`, the view picker in the terminals, the Task Scheduler task across a logon and after a crash, `a-stack update` over a running service, Windows Defender and SmartScreen with the unsigned binary, and the hooks with Claude Code and Codex. Please [open an issue](https://github.com/mzmzeee/a-stack/issues) when something does not work.

## Requirements

You need these only to build from source:

- Linux, macOS 13 or later, or Windows 10 or later
- Go 1.27.3 with cgo (`CGO_ENABLED=1`) and a C compiler. On macOS, install the Xcode command line tools with `xcode-select --install`. A binary built without cgo cannot open its database: `serve` stops and says that it was built without cgo. On Windows, put a MinGW-w64 `gcc`, such as the one from [llvm-mingw](https://github.com/mstorsjo/llvm-mingw) or MSYS2, on your `PATH`.

SQLite 3.53.4 is built into the binary from `github.com/mattn/go-sqlite3`. Do not build with the `libsqlite3` tag.

## Build from source

Run this command in the repository root:

```sh
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
```

This binary uses your system's C library. To build the static release binary, see [Releasing](../CONTRIBUTING.md#releasing).

## Uninstall

If you set up the systemd user service, the launchd agent or the Task Scheduler task, stop it and remove the unit, the plist or the task:

```sh
a-stack service uninstall
```

On macOS, also remove the log if you do not need it: `rm ~/Library/Logs/a-stack.log`. On Windows, the log is `%LOCALAPPDATA%\a-stack\service.log`.

If you added a drop-in, remove it too: `rm -rf ~/.config/systemd/user/a-stack.service.d`. Without `a-stack service`, run these commands:

```sh
systemctl --user disable --now a-stack
rm -f ~/.config/systemd/user/a-stack.service
rm -rf ~/.config/systemd/user/a-stack.service.d
systemctl --user daemon-reload
```

If no other user service needs linger, you can also turn it off with `loginctl disable-linger "$USER"`.

Remove the binary:

```sh
rm ~/.local/bin/a-stack
```

On Windows, remove the folder of the install script with `Remove-Item -Recurse $env:LOCALAPPDATA\Programs\a-stack`, and remove it from your user `PATH` in Settings, under "Edit environment variables for your account". Delete the hook cursor files with `Remove-Item -Recurse $env:LOCALAPPDATA\a-stack-hooks` if they exist.

This does not remove your data. The data stays in the state directory, `~/.local/state/a-stack` (or `$XDG_STATE_HOME/a-stack`; on Windows `%LOCALAPPDATA%\a-stack`). Remove that directory only if you do not need the data or the Operator credential again.
