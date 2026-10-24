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

It runs formatting, linters, vulnerability and secret scans, the tests with coverage, the race tests, the build and the quick validation run. [Quality checks](docs/quality.md) lists the tool versions and rule choices. After a change that can affect speed or storage, also run the full [validation runner](docs/validation.md).

## Releasing

Before you tag, move the Unreleased entries in `CHANGELOG.md` into a section for the new version. The release uses that section as its notes, and stops if it is missing.

Push a version tag from `main`:

```sh
git tag v0.1.0
git push origin v0.1.0
```

`.github/workflows/release.yml` runs every check, builds a static binary for `amd64` and `arm64`, and publishes a GitHub release. The release has `a-stack_linux_amd64.tar.gz`, `a-stack_linux_arm64.tar.gz`, `install.sh`, `a-stack.service` and `SHA256SUMS`. A tag with a suffix, such as `v0.2.0-rc.1`, becomes a prerelease, which the installer's `latest` URL skips.

To build the release archive for your own machine, run `scripts/build-release.sh v0.1.0`. It needs Docker and writes `dist/a-stack_linux_ARCH.tar.gz`.

## Update the demo

After a change to the browser view, record the README demo again:

```sh
scripts/record-demo.sh
```

The script builds a-stack, plays a short scripted Session on a temporary service and records it with agent-browser. It writes `docs/images/demo.gif`, `graph-light.png`, `graph-dark.png` and `agents.png`, and deletes the temporary state when it ends. It needs Go, ffmpeg, agent-browser and ImageMagick (`magick`). It does not need Docker.

## Design records

Read these before you change how a-stack behaves:

- [Glossary](GLOSSARY.md): the meaning of Operator, Orchestrator, Task, Session and the other terms.
- Architecture decision records:
  - [0001: Orchestrator authority and Task rules](docs/adr/0001-graph-only-approval.md)
  - [0002: Local Go service and SQLite](docs/adr/0002-local-service-and-sqlite.md)
  - [0003: Retained shared Task mail](docs/adr/0003-retained-task-mail.md)
  - [0004: Herdr mention notices](docs/adr/0004-herdr-notifications.md)
  - [0005: Read-only browser](docs/adr/0005-read-only-browser.md)
- [Visual prototype](prototypes/h-stack-visual/README.md): the approved design reference for the browser view.
- Research notes in [`docs/research/`](docs/research/): background evidence. Their proposals do not override the specification or the decision records.
