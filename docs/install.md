# Install

a-stack runs on Linux, on x86-64 (`amd64`) and ARM64 (`arm64`). The release binaries are statically linked. They do not need other libraries.

## Install script

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
```

The script does these steps:

1. It finds the archive for your processor.
2. It downloads the archive and the release's `SHA256SUMS` file.
3. It checks the signature of `SHA256SUMS` with OpenSSL 3, if OpenSSL 3 is installed. See [How releases are verified](#how-releases-are-verified).
4. It compares the checksum. If the signature or the checksum is not correct, it stops and installs nothing.
5. It installs the binary in `~/.local/bin`, without `sudo`.
6. It tells you if this folder is not on your `PATH`.

To change the version or the folder, set these variables:

| Variable | Default | Example |
| --- | --- | --- |
| `A_STACK_VERSION` | the latest release | `v0.1.0` |
| `A_STACK_INSTALL_DIR` | `~/.local/bin` | `$HOME/bin` |

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | A_STACK_VERSION=v0.1.0 A_STACK_INSTALL_DIR="$HOME/bin" sh
```

To read the script before you run it, download it first:

```sh
curl -fsSL -o install.sh https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh
less install.sh
sh install.sh
```

## Manual download

1. Open the [releases page](https://github.com/mzmzeee/a-stack/releases).
2. Download `a-stack_linux_amd64.tar.gz` or `a-stack_linux_arm64.tar.gz`, `SHA256SUMS` and `SHA256SUMS.sig`.
3. Check the archive, either with the release signature or with the GitHub attestation. See [How releases are verified](#how-releases-are-verified). Then check the checksum:

   ```sh
   sha256sum --check --ignore-missing SHA256SUMS
   ```

4. Unpack the binary into a folder on your `PATH`:

   ```sh
   tar -xzf a-stack_linux_amd64.tar.gz -C ~/.local/bin a-stack
   ```

## Go install

With Go installed, you can build and install a release with one command:

```sh
CGO_ENABLED=1 go install github.com/mzmzeee/a-stack/cmd/a-stack@latest
```

- It needs a C compiler, such as `gcc`, because SQLite is built in with cgo. Without one, the build fails.
- It needs Go 1.27.3 or later. An older `go` downloads that version itself, unless `GOTOOLCHAIN=local` is set.
- It installs the binary in `$(go env GOBIN)`, or `~/go/bin` when `GOBIN` is not set. Make sure that folder is on your `PATH`.
- The build takes about a minute. `a-stack version` shows the release, such as `v0.1.4`. Use `@vX.Y.Z` instead of `@latest` for another version.
- The binary uses your system's C library, like a [build from source](#build-from-source). It does not check the release signature; Go checks the module against the Go checksum database instead.

`a-stack update` replaces this binary with the release binary, in the same folder.

## Package managers

An AUR package (`a-stack-bin`) and a Homebrew formula for Linux are prepared in [`contrib/`](../contrib/README.md), but not published yet. Both install the release binary and check it against the release's `SHA256SUMS`. If you install a-stack with a package manager, update it with that package manager: `a-stack update` cannot replace a binary in `/usr/bin`.

## How releases are verified

The release workflow on GitHub Actions builds every release. It signs the `SHA256SUMS` file with an Ed25519 key that only the workflow can use, and uploads the signature as `SHA256SUMS.sig`. The file has the raw 64-byte signature. The public key is in `install.sh` and in the `a-stack` binary:

```text
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEANkJiNRRt3iLovFX+EVrAxA1Z7a3diAw6F3PhyC9+D08=
-----END PUBLIC KEY-----
```

- `a-stack update` checks the signature before it trusts `SHA256SUMS`. A missing or wrong signature stops the update, and nothing changes.
- The install script checks it with OpenSSL 3 or later. Without OpenSSL 3, it prints one line that it did not check the signature, and checks only the checksum.
- Releases up to v0.1.4 have no signature. Both accept them with a warning, but only when you name such a version: `a-stack update --version v0.1.4` or `A_STACK_VERSION=v0.1.4`. For v0.1.5 and later, a missing signature is an error.

To check a signature by hand, save the public key above as `a-stack-release.pem` and run:

```sh
openssl pkeyutl -verify -pubin -inkey a-stack-release.pem -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig
```

It prints `Signature Verified Successfully`. Then check the archive with `sha256sum --check --ignore-missing SHA256SUMS`.

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

   If you installed the agent skill with `a-stack skill install`, the update also updates those copies, in your home folder and in the current folder. It does not create new ones.

   - `a-stack update --check` only compares the versions. When a newer release exists, it prints `a-stack v0.1.4 is available; you have v0.1.3. Run a-stack update to install it.` Otherwise it says that your version is the latest. It exits with 0 in both cases. With `--json`, `update_available` is `true` or `false`.
   - `a-stack update --version v0.1.2` installs that release, also an older one. For an older version it prints a warning: an older a-stack may not open the current database. See [Go back to an older version](#go-back-to-an-older-version).
   - A build from source has no release version. It updates only with `--version`.
   - If you cannot write to the binary's folder, the command stops and names the folder. Install a-stack in a folder that you own with the install script and `A_STACK_INSTALL_DIR`.
   - `A_STACK_BASE_URL` names a release mirror, as for the install script.

   Releases up to v0.1.3 do not have `a-stack update`. To update them, or as the other way, run the install script again:

   ```sh
   curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
   ```

   To pick a version with the script, set `A_STACK_VERSION` as in [Install script](#install-script).

3. Restart the service. Until then, the running service keeps the old version.

   - If you started `a-stack serve` by hand, press Ctrl-C in its terminal and run `a-stack serve` again.
   - If systemd runs the service, run `systemctl --user restart a-stack`.

4. If the open browser tab does not find the service again, open the view again. This command opens your last Session:

   ```sh
   a-stack
   ```

During the restart, Agent commands fail with `storage_unavailable` and `retryable: true` for a few seconds. An Agent can run the same command again with the same operation ID. a-stack makes the change only one time.

When the new version has a new database schema, the first start migrates the database. Before that, it writes a private copy of the database, `state.db.pre-vN`, in the state directory (N is the old schema version).

### Go back to an older version

An older a-stack cannot open a database that a newer one migrated. `a-stack serve` then stops with exit code 4 and a message that names the schema version it found and the newest one it supports. The systemd unit does not restart it. To get back:

- Update to the newer version again: `a-stack update`, or `a-stack update --version vX.Y.Z`.
- Or, to stay on the older version, restore the `state.db.pre-vN` copy or a backup made with the older version, as in [Backup and restore](foundation.md#backup-and-restore). Changes made after that copy are lost.

## Start a-stack automatically

On Linux with systemd, a user service can run `a-stack serve` for you. systemd starts it when your user manager starts, and starts it again if it crashes. You do not need `sudo`.

The unit runs `~/.local/bin/a-stack`. If you installed the binary in another folder, change the path in the `ExecStart` line.

1. Stop any `a-stack serve` that you started by hand. Only one service can use a state directory. A second one stops with `conflict`. The unit then shows as failed with exit code 3 and does not restart. It also does not restart after exit code 4, when the database is damaged or has a newer schema.

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

   Without linger, systemd starts your user services only when you log in, and stops them when you log out.

To read the service's log, run `journalctl --user -u a-stack`. At each start, the service writes one ready line with its URL.

### Use the same state directory as the CLI

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

### Stop the service

To stop the service until the next boot or login, run `systemctl --user stop a-stack`. systemd starts the service again only when it fails, so it stays stopped.

To stop the service and not start it automatically again, run `systemctl --user disable --now a-stack`. To remove the unit, see [Uninstall](#uninstall).

## Requirements

You need these only to build from source:

- Linux
- Go 1.27.3 with cgo (`CGO_ENABLED=1`) and a C compiler

SQLite 3.53.4 is built into the binary from `github.com/mattn/go-sqlite3`. Do not build with the `libsqlite3` tag.

## Build from source

Run this command in the repository root:

```sh
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
```

This binary uses your system's C library. To build the static release binary, see [Releasing](../CONTRIBUTING.md#releasing).

## Uninstall

If you set up the systemd user service, stop it and remove the unit and any drop-in:

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

This does not remove your data. The data stays in the state directory, `~/.local/state/a-stack` (or `$XDG_STATE_HOME/a-stack`). Remove that directory only if you do not need the data or the Operator credential again.
