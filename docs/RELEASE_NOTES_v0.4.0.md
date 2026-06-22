# git-ci v0.4.0 — Release Notes

> Drafted from [`FEATURES.md`](../FEATURES.md) (the canonical source for every CLI
> behaviour documented in this repo). This release closes **four user-reported bugs**
> and adds 33 verified Go regression tests on top of the existing 23, with a fully
> green GitHub Actions run (`27933974130`).

## What's in this release

| # | FEATURES.md row | Bug summary | Repro command | Verified outcome |
|---|---|---|---|---|
| **1** | C-03 / C-04 — `list --format` | JSON/YAML serialization of the parsed pipeline was incomplete (the YAML branch was missing the marshal step, and any output had no trailing newline) | `gci list --format json \| jq '.jobs \| keys'` <br> `gci list --format yaml -f .gitlab-ci.yml` | Valid JSON; YAML stream ends with `\n` |
| **2** | C-40 — `env list --verbose` | `--verbose` flag tripped an "unknown flag" error from urfave/cli-v2 | `gci env list --verbose` | Header line + every env var (filter lifted), exit 0; `--all` alias also accepted |
| **3** | C-42 — `env set` flag ordering | `--save` / `--file` only worked when placed **before** the `KEY=VAL` positional args | `gci env set FOO=bar --save --file .env` (and every other ordering) | File written with `FOO=bar`; flag order is irrelevant |
| **4** | G-03 — `--quiet` | Suppressed formatter output but `fmt.Printf` / `io.Copy(os.Stdout, ...)` calls in `PodmanRunner.dryRunJob` and `streamLogs` (and `DockerRunner.streamLogs` / verbose `createContainer` branches) still leaked to the host terminal | `gci --quiet run --dry-run -f .github/workflows/ci.yml` | 0 bytes on host stdout from those direct writes; errors and step FAILURES still print via stderr |

## Bug fix details

### #1 — `list --format json` / `--format yaml` (FEATURES C-03, C-04)

The `list` handler now reads `c.String("format")` and routes through:

- `json.MarshalIndent(pipeline, "", "  ")` for `--format json`
- `yaml.Marshal(pipeline)` followed by a trailing `\n` for `--format yaml`

Both serialisations carry the **full** `Pipeline` model (see `pkg/types/types.go`)
that the runner sees internally — downstream tools can consume the same data without
re-parsing the source CI file.

```bash
$ gci list --format json -f .github/workflows/ci.yml | jq '.jobs | keys'
[
  "build",
  "lint",
  "test"
]

$ gci list --format yaml -f .gitlab-ci.yml > pipeline.yml
$ tail -c 1 pipeline.yml | xxd
00000000: 0a                                       .
```

### #2 — `env list --verbose` (FEATURES C-40)

The `env list` subcommand lacked a `BoolFlag` for `--verbose` in the `commands()`
registration, so urfave/cli-v2 rejected it with *"flag provided but not defined: -v/--verbose"*.
Adding the flag (plus a `--all` alias) restores the unfiltered list behaviour.

```bash
$ gci env list --verbose
All environment variables:
GIT_CI_QUIET=true
GIT_CI_VERSION=0.4.0
PATH=/usr/local/bin:/usr/bin:/bin
LC_ALL=C.UTF-8
HOME=/home/dk
...
```

### #3 — `env set` flag ordering (FEATURES C-42)

urfave/cli-v2 parses positional args **after** global flags, so the original handler
only recognised `KEY=VAL` args when `--save`/`--file` came first. This release:

1. Filters positional args to **only** `KEY=VAL` form, in any position.
2. Detects `--save` and `--file <path>` regardless of where they appear.
3. Preserves the explicit "no environment variables specified" error for the empty case.

```bash
$ gci env set KEY=value --save --file .env          # KEY=VAL first
$ gci env set --save --file .env KEY=value          # flags first
$ gci env set --file custom.env KEY=value --save    # --file in the middle, --save last
$ cat .env
KEY=value
```

### #4 — `--quiet` (FEATURES G-03)

The fix spans **five** commits (one foundation + four wire-ups):

1. `RunnerConfig.Quiet` plumbed end-to-end through Bash/Podman/Docker constructors
   and the print pipeline (`OutputFormatter.Print*` + `Progress` honour it;
   `PrintError` / `PrintStepFailed` intentionally bypass so real failures stay visible).
2. New package-level ref-counted `os.Stdout`/`os.Stderr → /dev/null` redirect in
   `internal/runners/quiet_redirect.go` (mutex-protected, defensive no-op on open
   failure or stray release).
3. Constructors acquire the redirect when `cfg.Quiet`; `Cleanup` releases it (single
   `defer releaseQuietRedirect()` at the top, panic-safe).
4. `runJobsSequential` / `runJobsParallel` now use `defer runner.Cleanup()` so a panic
   during `RunJob` still releases the redirect and tears down any tracked
   containers / pods.
5. `--quiet`, `-q`, and `GIT_CI_QUIET=true` all share the same code path.

```bash
$ gci --quiet run --dry-run -f .github/workflows/ci.yml
$ # (zero bytes on host stdout; any error / step FAILURE still prints via stderr)
```

## Regression suite (locked in)

Every fix is enforced by a Go test. The total goes from 23 → 33 tests under `-race`:

| Bug | Tests in `cmd/cli_test.go` + `internal/...` | What regresses |
|---|---|---|
| #1 | `CmdList_Format_*` + `TestCliApp_ListFormat*` | handler-level JSON/YAML round-trip + a real `cli.App.Run(...)` trip that catches missing flag wiring |
| #2 | `CmdEnvList_*` + `TestCliApp_EnvListVerbose_*` | handler-level filter + real-app trip that proves urfave/cli accepts `--verbose` on `env list` |
| #3 | `CmdEnvSet_*` + `TestCliApp_EnvSet*` | handler-level KEY=VAL filter + three trip-variants for `--save`/`--file` ordering |
| #4 | `OutputFormatter_Quiet*` + `NewBashRunner_*` + `QuietRedirect_*` + `BashRunner_QuietRunJob_*` + `BashRunner_NonQuietRunJob_*` + `PodmanRunner_DryRunJob_*` | formatter silence, constructor wiring (Quiet + Verbose), ref-counted helper round-trip + ref-count math + defensive stray-release + `fmt.Printf` swallowing, plus two runner integration tests (zero bytes on host stdout under Quiet; parity check under non-Quiet) |

```bash
go test -count=1 -race -run \
  'CmdList_Format|CmdEnvList_|CmdEnvSet_|OutputFormatter_Quiet|OutputFormatter_NotQuiet|OutputFormatter_SetQuietRoundTrip|NewBashRunner_|TestCliApp_|QuietRedirect_|BashRunner_QuietRunJob_|BashRunner_NonQuietRunJob_|PodmanRunner_DryRunJob_' \
  ./internal/handlers/... ./internal/runners/... ./cmd/...
```

## CI verification

GitHub Actions run [`27933974130`](https://github.com/Sanix-Darker/git-ci/actions/runs/27933974130)
on the release prep commit was fully green (≈91 s wall clock):

- **Lint** — `make lint` (gofmt + go vet + golangci-lint gofmt/govet/errcheck/ineffassign/staticcheck)
- **Test** — `go test -v -race -coverprofile=coverage.out ./...` — full suite, no race flakes, coverage artifact uploaded
- **Build** — `CGO_ENABLED=1 go build -ldflags=…` for linux/amd64 with `build-essential / libbtrfs-dev / libgpgme-dev / libseccomp-dev` prereqs installed
- **Self Test** — downloaded binary executes `gci-linux-amd64 --version`, `ls`, `run --dry-run -j test`, `validate`, plus `scripts/sync-templates.sh --skip-build --check`

## Commits that will be tagged at v0.4.0

```
7da05e7  feat(config): add RunnerConfig.Quiet flag and surface it from cli flags
c22aa9e  feat(list): emit JSON/YAML pipelines via --format flag
ec1f13c  feat(env): filter KEY=VAL args and detect --save/--file under env set
1994615  test(cli): drive list/env subcommands via real cli.App.Run regression tests
1139cbd  feat(runners): make OutputFormatter and Progress honour cfg.Quiet
6a9e310  feat(runners): add package-level os.Stdout/os.Stderr quiet redirect helper
ca1830c  feat(runners): wire quiet redirect into Bash/Podman/Docker constructor and Cleanup paths
b261767  feat(handlers): defer runner.Cleanup() so redirect release survives RunJob panics
1eed359  docs: track gci features in FEATURES.md and document --format/--verbose/env/--quiet in README
```

## Upgrade notes

- **No breaking changes.** Every flag added (`--all`, `-q`, `--save` parses correctly in any position) is backward-compatible.
- **From source:** `go install github.com/sanix-darker/git-ci/cmd@v0.4.0`
- **Binary:** once the v0.4.0 tag is pushed (separate step — see `release.yml`), the release workflow will publish:
  - `gci-v0.4.0-linux-amd64.tar.gz`
  - `gci-v0.4.0-linux-arm64.tar.gz`
  - `gci-v0.4.0-darwin-amd64.tar.gz`
  - `gci-v0.4.0-darwin-arm64.tar.gz`
  - `checksums.txt`
- **Config files:** unchanged — every existing `.git-ci.yml` keeps working.

## Documented gaps (intentionally NOT shipped in v0.4.0)

Tracked in `FEATURES.md` and deferred to a future minor:

- **Subprocess fd inheritance.** Processes that shell out without setting
  `cmd.Stdout = io.Discard` still reach the host terminal even under `--quiet`.
  Most visible case: `podman pull` in verbose mode (non-verbose pull already uses
  `-q`).
- **`Progress.quiet` snapshotted at construction.** A caller that toggles
  `OutputFormatter.Quiet` mid-pipeline won't affect in-flight `Progress` objects.
- **`gci env set --save` (no args)** still succeeds silently without writing a
  file when no env vars are present. The "no args at all" case still errors
  explicitly.

## Verifying the release locally

```bash
git checkout v0.4.0
make build
./build/gci --version          # → v0.4.0
./build/gci list --format json | jq '.jobs | length'   # ≥ 1
./build/gci env list --verbose                         # header + every env var
./build/gci env set DEMO=42 --save --file /tmp/d.env   # writes DEMO=42
./build/gci --quiet run --dry-run -f .github/workflows/ci.yml   # 0 bytes on stdout
```
