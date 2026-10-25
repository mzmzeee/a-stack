# Contributing

## Issues and specs

Specs and tickets live in [GitHub Issues](https://github.com/mzmzeee/a-stack/issues). The [v1 specification](https://github.com/mzmzeee/a-stack/issues/1) is the source of truth for v1 behavior.

## Development setup

You need the Go toolchain from the [install requirements](docs/install.md#requirements), plus:

- Python 3 and `findmnt`, for the validation runner
- Node.js with `npx`, for oxlint
- `uv`, for Ruff, ShellCheck and zizmor

Build and test:

```sh
GOTOOLCHAIN=go1.27.3 CGO_ENABLED=1 go build -o a-stack ./cmd/a-stack
go test ./...
go test -race ./...
```

The tests use temporary state, real SQLite and HTTP, and CLI subprocesses. They never contact real Agent terminals.

## Checks

Run every check that CI runs:

```sh
scripts/check.sh
```

It runs formatting, linters, vulnerability and secret scans, the tests with coverage, the race tests, the build and the quick validation run. It runs on Linux only; CI also builds, vets and tests a-stack on macOS and Windows. To check that the Windows code compiles on Linux, run `GOOS=windows CGO_ENABLED=0 go vet ./...`. [Quality checks](docs/quality.md) lists the tool versions and rule choices. After a change that can affect speed or storage, also run the full [validation runner](docs/validation.md) and the [stress test](docs/performance.md#run-it).

## Releasing

Before you tag, move the Unreleased entries in `CHANGELOG.md` into a section for the new version. The release uses that section as its notes, and stops if it is missing.

Push a version tag from `main`:

```sh
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` runs every check, builds a static Linux binary, a macOS binary and a Windows binary for `amd64` and `arm64`, and publishes a GitHub release. The macOS builds run on the `macos-15` (Apple silicon) and `macos-15-intel` runners, because SQLite needs cgo. Both Windows builds cross-compile on `ubuntu-24.04` with a pinned llvm-mingw, and a smoke test runs them on `windows-latest` and `windows-11-arm` before the release is published. The release has `a-stack_linux_amd64.tar.gz`, `a-stack_linux_arm64.tar.gz`, `a-stack_darwin_amd64.tar.gz`, `a-stack_darwin_arm64.tar.gz`, `a-stack_windows_amd64.zip`, `a-stack_windows_arm64.zip`, `install.sh`, `install.ps1`, `a-stack.service`, `a-stack-skill.tar.gz` (the agent skill), `SHA256SUMS` and `SHA256SUMS.sig`. The workflow signs `SHA256SUMS` with the Ed25519 key in the `A_STACK_SIGNING_KEY` Actions secret (PEM), checks the signature with the public key in `install.sh`, and attests every file. `a-stack update` has the same public key in `cmd/a-stack/update.go`; a new key needs a change in both places. A tag with a suffix, such as `v0.2.0-rc.1`, becomes a prerelease, which the installer's `latest` URL skips. If a release for the tag exists already, the workflow leaves it unchanged and prints a notice. To publish it again, delete the release first.

To build the release archive for your own machine, run `scripts/build-release.sh v0.1.0`. It writes `dist/a-stack_OS_ARCH.tar.gz`. On Linux it needs Docker. On macOS it needs Go 1.27.3 and the Xcode command line tools, and the binary runs on macOS 13 or later. The `.tar.gz` archives name owner 0 and the time of the commit (or `SOURCE_DATE_EPOCH`), so the same binary gives the same archive. `scripts/build-release.sh v0.1.0 windows amd64` (or `arm64`) cross-builds `dist/a-stack_windows_ARCH.zip` on Linux; it needs an x86-64 Linux with Go 1.27.3, `curl`, `xz` and `zip`, and downloads llvm-mingw 20261006 into `.tmp/` once, checked against a pinned SHA-256. The script pins the Go image and the versions of the Alpine packages it installs. When Alpine drops one of these versions, the build stops at `apk add`; then pin the new version.

## Update the demo

After a change to the browser view or the Operator commands, record the README demo again:

```sh
scripts/record-demo.sh
```

The script builds a-stack and runs a short Session on a temporary service, with a temporary HOME, explicit disposable state and private hook settings/cache. It records one story: a real terminal (`scripts/demo-terminal.py`) and the browser view (agent-browser) at the same time. Terminal helpers shorten mutation receipts and show only the first line of a Stop guard’s reason; the hook events, replies and Task changes are real. `scripts/demo-overlay.js` draws the outlines and callouts and zooms in, in the recording browser only. Then it cuts the scenes into GIFs, each scene under its caption. To keep the GIFs small at the same pace, it drops frames that do not change and shows the one before longer, keeps every third frame while the view zooms or scrolls, and uses 64 colors and gifsicle's `-O3 --lossy=40`. The footer of the view shows the latest release tag (`git describe --tags --abbrev=0`), not the development build. The scene list at the top of the script names the GIF, the tab, the terminal and the caption of each scene. To try a change without touching the images, set `DEMO_OUT` to another folder. In `docs/images` it writes `demo.gif` (the README tour), `demo-graph.gif`, `demo-archive.gif` and `demo-agents.gif` (the clips in the browser view guide), the stills `graph-light.png`, `graph-dark.png`, `graph-overview.png` (minimap and semantic zoom), `now-light.png`, `now-dark.png`, `mail-light.png`, `mail-dark.png` and `agents.png`, and `picker.png`. It deletes the temporary state when it ends. It needs Go, Python 3, ffmpeg, agent-browser, ImageMagick (`magick`), gifsicle and curl. It downloads a pinned release of [agg](https://github.com/asciinema/agg) into `.tmp/` and checks its SHA-256. It does not need Docker.
