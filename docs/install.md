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

Remove the binary:

```sh
rm ~/.local/bin/a-stack
```

This does not remove your data. The data stays in the state directory, `~/.local/state/a-stack` (or `$XDG_STATE_HOME/a-stack`). Remove that directory only if you do not need the data or the Operator credential again.
