# Quality checks

Run every check with one command from the repository root:

```sh
scripts/check.sh
```

The script stops at the first failure and exits nonzero. A full run takes about 70 seconds on the development machine. It writes coverage, the built binary, installed tools and the quick validation result under `.tmp/`, which Git ignores.

## Prerequisites

- Go 1.27.3 toolchain, `CGO_ENABLED=1` and a C compiler (the script sets `GOTOOLCHAIN` and `CGO_ENABLED`).
- Python 3 and `findmnt` for the validation runner.
- Node.js with `npx`, for oxlint.
- `uv` with `uvx`, for Ruff and ShellCheck.
- Network access the first time each tool installs, and on every run for the Go vulnerability database.

The script does not change `go.mod` and installs nothing globally. Go tools go to `.tmp/bin`. npx and uvx keep their normal per-user caches.

## What runs

| Step | Tool and version | Checks |
| --- | --- | --- |
| Formatting | `gofmt` (Go 1.27.3) | Go files in `cmd/` and `internal/` are formatted. |
| Go analysis | `go vet`, Staticcheck 2026.2.2 (`v0.8.2`) | Default Staticcheck checks. |
| Vulnerabilities | govulncheck `v1.9.0` | Known Go advisories reachable from this module. |
| Tests | `go test -count=1 -coverprofile=.tmp/coverage.out ./...` | Full suite, fresh, with a coverage summary. |
| Races | `go test -race -count=1 ./...` | Full suite under the race detector. |
| Build | `go build -o .tmp/a-stack ./cmd/a-stack` | The product binary. |
| Browser script | `node --check`, oxlint 1.89.0 | Syntax, then the rules in `.oxlintrc.json`. |
| Validation runner | `python3 -m py_compile`, Ruff 0.17.2 | Syntax, then the rules in `ruff.toml`. |
| Check script | ShellCheck 0.11.0 | `scripts/check.sh` itself. |
| CI config | actionlint 1.7.12, zizmor 1.31.1 | `.github/` workflow syntax and workflow security. |
| Secrets | gitleaks 8.30.1 | The Git history of the checkout. |
| Workflow | `scripts/validate-v1.py --quick` | The disposable end-to-end workflow and engine fault tests against `.tmp/a-stack`. |

To change a tool version, edit the variable at the top of `scripts/check.sh` and this table.

## CI

`.github/workflows/check.yml` runs `scripts/check.sh` on Ubuntu 24.04 for every pull request and every push to `main`. Its actions are pinned by commit SHA. `.github/dependabot.yml` proposes weekly updates for the Go module and the pinned actions. Dependabot does not change the tool versions in `scripts/check.sh` or the Go version in `go.mod` and `.go-version`; update those by hand.

## Rule choices

- **Staticcheck** runs its default checks. One finding is suppressed in place: the empty critical section in `Service.Close` (`internal/stack/http.go`) waits for an HTTP-started notice drain to finish before the database closes.
- **oxlint** uses the `correctness` category plus `no-undef` and `no-unused-vars`, with browser globals. The `suspicious` category was left out: on `app.js` it only reported style preferences, such as `toSorted()` instead of sorting a freshly built array.
- **golangci-lint** was tried (v2.15.0) and left out. Beyond `go vet` and Staticcheck, it reported unchecked deferred `Close` and `Rollback` calls, error type assertions on errors the code never wraps, and style quick fixes. None was a defect.
- **Ruff** selects syntax errors, Pyflakes (`F`), flake8-bugbear (`B`) and Pylint errors (`PLE`). It selects no style rules. Six lines in `scripts/validate-v1.py` suppress `B023`, because those closures run at once inside the loop iteration that defines them.

## Known limits

- govulncheck checks Go code only. It does not check the SQLite C source bundled in `github.com/mattn/go-sqlite3`. For SQLite, compare the bundled version (see [foundation](foundation.md)) with the [SQLite CVE list](https://www.sqlite.org/cves.html) when you upgrade.
- Coverage shows where tests run, not whether they check the right behavior. There is no coverage threshold.
- No tool checks `app.css` or `index.html`. They are small, and the browser checks in [validation](validation.md) cover their rendering.
- `validate-v1.py --quick` skips the timing and storage measurements. Run the full runner, as described in [validation](validation.md), after a change that can affect performance or storage.
- The runner and the tests use a fake Herdr. They do not prove delivery to a real agent; see [notifications](notifications.md).
