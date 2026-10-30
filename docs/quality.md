# Quality checks

Run every check with one command from the repository root:

```sh
scripts/check.sh
```

The script stops at the first failure and exits nonzero. Runtime depends on the machine, tool caches and test load. It writes coverage, the built binary, installed tools and the quick validation result under `.tmp/`, which Git ignores.

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
| Go formatting | gofumpt `v0.12.0`, golangci-lint `v2.15.0` | Go formatting, import groups and readability rules, including tests and the `bench` build tag. |
| Other formatting | Prettier `3.9.10`, Ruff `0.17.2`, shfmt `v3.14.1` | Embedded JS/CSS/HTML, `scripts/*.js`, `scripts/*.py`, `install.sh` and `scripts/*.sh`. |
| Go analysis | `go vet`, Staticcheck 2026.2.2 (`v0.8.2`) | Default Staticcheck checks. |
| Vulnerabilities | govulncheck `v1.9.0` | Known Go advisories reachable from this module. |
| Tests | `go test -count=1 -coverprofile=.tmp/coverage.out ./...` | Full suite, fresh, with a coverage summary. |
| Races | `go test -race -count=1 ./...` | Full suite under the race detector. |
| Build | `go build -o .tmp/a-stack ./cmd/a-stack` | The product binary. |
| Browser script | `node --check`, oxlint 1.89.0 | Syntax, then the rules in `.oxlintrc.json`. |
| Validation runner | `python3 -m py_compile`, Ruff 0.17.2 | Syntax, then the rules in `ruff.toml`. |
| Shell scripts | ShellCheck 0.11.0 | `scripts/check.sh`, `scripts/build-release.sh` and `install.sh`. |
| CI config | actionlint 1.7.12, zizmor 1.31.1 | `.github/` workflow syntax and workflow security. |
| Secrets | gitleaks 8.30.1 | The Git history of the checkout. |
| Workflow | `scripts/validate-v1.py --quick` | The disposable end-to-end workflow and engine fault tests against `.tmp/a-stack`. |

To change a tool version, edit the variable at the top of `scripts/check.sh` and this table.

## CI

`.github/workflows/check.yml` runs `scripts/check.sh` on Ubuntu 24.04 for every pull request and every push to `main`. Its actions are pinned by commit SHA. `.github/dependabot.yml` proposes weekly updates for the Go module and the pinned actions. Dependabot does not change the tool versions in `scripts/check.sh` or the Go version in `go.mod` and `.go-version`; update those by hand.

## Regression gates

Every fix ships a scenario that fails without it. Run that scenario against the parent revision or a narrowly seeded fault in a disposable copy, then against the fix. Coverage alone does not establish this. Closed-session stories first prove each enrolled harness can block while open, then clear its one-shot marker before checking silence. Browser peeks compare all persisted database rows after view redemption and preserve the Agent's cursor file byte for byte. Snapshot URI tests cover Windows drive paths on every platform.

- **PR:** existing checks, deterministic testscript CLI stories, a short rapid graph/mail model run (100 histories), and a guard against test-only modules entering production dependencies. All 15 stories run, including exact-runtime enrollment boundaries. Platform-dependent symlink assertions run only where symlinks are supported; the other path assertions always run.
- **Nightly:** `.github/workflows/nightly.yml` runs only on its schedule or manual dispatch, never on a PR. Each of six fuzz targets runs for five minutes. The recovery job runs seeds 0–99 through SIGKILL at body, mention, receipt or post-commit milestones, then 1,000 rapid histories. Logs and failing corpora are retained as artifacts for 14 days.
- **Release:** the release workflow calls the PR checks before building native artifacts. Required real-harness, service-manager and migration/restore checks remain separate release evidence; fake hook events and process kills do not prove native harness contracts or power-loss durability.

Run the crash workload locally with `A_STACK_CRASH_ITERATIONS=100 go test ./internal/stack -run TestMailSeededSIGKILLRecovery -count=1 -v`. Without the variable, it explicitly skips; the existing deterministic crash regression still runs.

`scripts/nightly-mutation.sh` pins Gremlins v0.6.0 and limits the pilot to permission, scope, revision and cursor predicates in `lifecycle.go`, `tasks.go` and `mail_reads.go`. It uses two workers and writes `.tmp/nightly/mutations.json` and a text log. `--dry-run` inventories mutants without executing them. A generous timeout multiplier accommodates compilation when the baseline is cached. The pilot is not a percentage gate: review lived and uncovered mutants, and report timeouts/nonviable mutants separately rather than count them as detected bugs. Tool errors still fail the job. No mutation tooling becomes a runtime module.

## Rule choices

- **Staticcheck** runs its default checks. One finding is suppressed in place: the empty critical section in `Service.Close` (`internal/stack/http.go`) waits for an HTTP-started notice drain to finish before the database closes.
- **oxlint** uses the `correctness` category plus `no-undef` and `no-unused-vars`, with browser globals. The `suspicious` category was left out: on `app.js` it only reported style preferences, such as `toSorted()` instead of sorting a freshly built array.
- **golangci-lint** enables only `whitespace`, `wsl_v5`, `misspell` and `godot`, plus the `gci` formatter (standard library, third-party and local imports). Staticcheck stays separate. `wsl_v5` separates control blocks and long-block returns; related setup can stay with a block that uses it (up to two statements), and short returns stay together (up to ten lines). Only `if`, `for`, `range`, `switch`, `type-switch`, `select`, `return` and `after-block` are enabled. The `assign` rule and other omitted rules would split related writes, setup, declarations and defers too often; `whitespace` already handles block edges. `nlreturn` is omitted because `wsl_v5` covers returns. `misspell` checks comments only, with neutral English; it must not rewrite strings or test inputs. `godot` checks declaration comments only, leaving inline labels alone.
- **Prettier** is the established formatter for all three browser languages. It uses two spaces, single quotes and semicolons for the browser/scripts. Installed OpenCode shims are outside the formatting scope so their released-content hashes stay unchanged. HTML uses its default whitespace-sensitive formatting. **shfmt** uses two spaces and indented case arms. **Ruff format** uses its defaults. Run `scripts/fmt.sh` to apply all fixes; `scripts/check.sh --format check` checks them without running the rest of the suite.
- **zizmor** has one inline suppression: `release.yml` calls `check.yml` with the `./` form instead of GitHub's newer `$/` self-repository form, because actionlint v1.7.12 rejects `$/`. Switch when actionlint supports it.
- **Ruff** selects its default set (`E4`, `E7`, `E9`, `F`) plus flake8-bugbear (`B`) and Pylint errors (`PLE`). It adds no formatting or line-length rules. Some lines in `scripts/validate-v1.py` suppress `B023`, because those closures run at once inside the loop iteration that defines them; a comment above the first one says so.

## Known limits

- govulncheck checks Go code only. It does not check the SQLite C source bundled in `github.com/mattn/go-sqlite3`. For SQLite, compare the bundled version (see the [install requirements](install.md#requirements)) with the [SQLite CVE list](https://www.sqlite.org/cves.html) when you upgrade.
- Coverage shows where tests run, not whether they check the right behavior. There is no coverage threshold.
- Prettier checks CSS and HTML formatting, not rendering or accessibility. The browser checks in [validation](validation.md) cover rendering.
- `validate-v1.py --quick` skips the timing and storage measurements. Run the full runner, as described in [validation](validation.md), after a change that can affect performance or storage.
- zizmor runs with `--offline`, so it does not check that each pinned action SHA matches its version comment. The SHAs in `.github/workflows/check.yml` were checked against the GitHub tags on 2026-10-24; Dependabot keeps them and their comments in step after that.
- The runner and the tests use a fake Herdr. They do not prove delivery to a real agent; see [notifications](notifications.md).
