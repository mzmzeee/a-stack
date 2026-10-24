# Install

a-stack runs on Linux, on x86-64 (`amd64`) and ARM64 (`arm64`). The release binaries are statically linked. They do not need other libraries.

## Install script

```sh
curl -fsSL https://github.com/mzmzeee/a-stack/releases/latest/download/install.sh | sh
```

The script does these steps:

1. It finds the archive for your processor.
2. It downloads the archive and the release's `SHA256SUMS` file.
3. It compares the checksum. If the checksum is not correct, it stops and installs nothing.
4. It installs the binary in `~/.local/bin`, without `sudo`.
5. It tells you if this folder is not on your `PATH`.

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
2. Download `a-stack_linux_amd64.tar.gz` or `a-stack_linux_arm64.tar.gz`, and `SHA256SUMS`.
3. Check the archive:

   ```sh
   sha256sum --check --ignore-missing SHA256SUMS
   ```

4. Unpack the binary into a folder on your `PATH`:

   ```sh
   tar -xzf a-stack_linux_amd64.tar.gz -C ~/.local/bin a-stack
   ```

## Check the installation

```sh
a-stack version
```

```json
{"data":{"version":"v0.1.0"}}
```

## Start a-stack automatically

On Linux with systemd, a user service can run `a-stack serve` for you. systemd starts it when your user manager starts, and starts it again if it crashes. You do not need `sudo`.

The unit runs `~/.local/bin/a-stack`. If you installed the binary in another folder, change the path in the `ExecStart` line.

1. Stop any `a-stack serve` that you started by hand. Only one service can use a state directory. A second one stops with `conflict`. The unit then shows as failed with exit code 3 and does not restart.

2. Download the unit into your systemd user folder:

   ```sh
   mkdir -p ~/.config/systemd/user
   curl -fsSL -o ~/.config/systemd/user/a-stack.service https://raw.githubusercontent.com/mzmzeee/a-stack/main/contrib/systemd/a-stack.service
   ```

   If you have a checkout of the repository, you can copy the unit from it instead:

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
