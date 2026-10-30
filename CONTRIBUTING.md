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

### Regression scenarios

Every fix ships a scenario that fails without it. Put CLI stories in `cmd/a-stack/testdata/scenarios/*.txtar` and run them with:

```sh
go test ./cmd/a-stack -run TestRegressionScenarios -count=1 -v
go test ./internal/stack -run TestRegressionModel -count=1 -rapid.checks=100
```

The testscript fixture builds the CLI once. Each story gets a fresh service, explicit state directory, private credentials, HOME and XDG/harness directories. It drops inherited service, credential and proxy selectors. It stops only its own service. Do not use real profiles or credentials in a scenario.

Use `as WORKER`, `as ORCH` or `as OPERATOR` followed by CLI arguments. `$TASK`, `$PROJECT`, `$SESSION` and `$WS` identify the fixture. `event HARNESS EVENT CWD SESSION` writes normalized Claude/Codex/Pi/OpenCode hook input to `$WORK/event.json`; `stdin $WORK/event.json` and `hook` feed it to the real CLI. These are fake event callers, not a claim that a native harness emitted the event. Keep assertions in the story; helpers should only arrange fixtures or check a boundary requiring a non-CLI capability. Never print credential files or view codes.

All 15 stories run after the `18-enroll` implementation. Identity stories explicitly enroll a native session and test unrelated sessions in the same folder, canonical paths, parent/subagent separation and rebinds. Existing hook unit tests still run. Story 09 checks foreign-settings preservation; it does not automatically repair legacy home-root installations.

`github.com/rogpeppe/go-internal` (testscript) and `pgregory.net/rapid` are pinned test-only dependencies. `TestProductionHasNoTestTooling` checks every non-test package's dependency closure, so neither may enter the shipped binary. The rapid test compares generated Task revisions, membership, graph edges and mail positions with an independent model. Use `-rapid.seed=SEED` to replay a failure; keep minimized inputs under `testdata/rapid` or as an ordinary regression.

Before submitting a fix, run the new story against a disposable copy with the fix reverted or a narrowly seeded fault. Record the failing assertion and the corrected run. Do not mutate a shared worktree for this check.

### Formatting

Run `scripts/fmt.sh` before committing. It applies pinned gofumpt, golangci-lint readability fixes and import grouping, Prettier for JS/CSS/HTML, Ruff for Python and shfmt for shell scripts. `scripts/check.sh` checks the same files without changing them; `scripts/check.sh --format check` runs only that gate. Versions live in `scripts/check.sh`; tools stay in `.tmp/bin` or the npx/uvx caches. `.editorconfig` sets indentation and preserves Markdown hard breaks. See [rule choices](docs/quality.md#rule-choices) for the whitespace settings.

Browser asset tests compare syntax tokens or extract snippets between token boundaries before running them in Node. Keep quoted values and exact visible-text assertions intact; do not rely on line breaks or indentation in JS, CSS or HTML.

### Parser fuzzing

`scripts/check.sh` runs each parser fuzz target for 3 seconds with two workers. Ordinary `go test ./...` runs their seed corpora without fuzzing. To investigate for longer, run each target separately, for example:

```sh
go test ./internal/stack -run '^$' -fuzz '^FuzzMentionNames$' -fuzztime=2m -parallel=2
go test ./cmd/a-stack -run '^$' -fuzz '^FuzzExtractZip$' -fuzztime=2m -parallel=2
```

The targets are `FuzzMentionNames`, `FuzzMailCursor` and `FuzzMailCheckCursor` in `internal/stack`, and `FuzzSplitFlags`, `FuzzExtractTarGz` and `FuzzExtractZip` in `cmd/a-stack`. They cover malformed text, quoting, duplicate flags, foreign and oversized cursors, damaged archives, unsafe paths and oversized archive entries. They do not execute CLI commands or write extracted files. If Go finds a failure, keep the regression input in the package's `testdata/fuzz/TARGET/` folder with the fix.

## Releasing

When changing the embedded skill or Pi/OpenCode shim files, add the SHA-256 hashes of their previous released contents to `installedHashes` in `cmd/a-stack/refresh.go`, keeping the older hashes. The update uses these to recognize unedited installed files; a filename or header alone is not enough. The hashes come from `git show vX.Y.Z:PATH | sha256sum`.

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

The script builds a-stack and runs a short Session on a temporary service, with a temporary HOME, explicit disposable state and private hook settings/cache. It records one story: a real terminal (`scripts/demo-terminal.py`) and the browser view (agent-browser) at the same time. Terminal helpers shorten mutation receipts and show only the first line of a Stop guard’s reason; the hook events, replies and Task changes are real. `scripts/demo-overlay.js` draws the outlines and callouts and zooms in, in the recording browser only. Then it cuts the scenes into GIFs, each scene under its caption. To keep the GIFs small at the same pace, it drops frames that do not change and shows the one before longer, keeps every third frame while the view zooms or scrolls, and uses 64 colors and gifsicle's `-O3 --lossy=40`. The footer of the view shows the latest release tag (`git describe --tags --abbrev=0`), not the development build. Before the next tag exists, override it with `DEMO_VERSION=v0.1.8 scripts/record-demo.sh`. The scene list at the top of the script names the GIF, the tab, the terminal and the caption of each scene. To try a change without touching the images, set `DEMO_OUT` to another folder. In `docs/images` it writes `demo.gif` (the README tour), `demo-graph.gif`, `demo-archive.gif` and `demo-agents.gif` (the clips in the browser view guide), the stills `graph-light.png`, `graph-dark.png`, `graph-overview.png` (minimap and semantic zoom), `now-light.png`, `now-dark.png`, `mail-light.png`, `mail-dark.png` and `agents.png`, and `picker.png`. It deletes the temporary state when it ends. It needs Go, Python 3, ffmpeg, agent-browser, ImageMagick (`magick`), gifsicle and curl. It downloads a pinned release of [agg](https://github.com/asciinema/agg) into `.tmp/` and checks its SHA-256. It does not need Docker.
