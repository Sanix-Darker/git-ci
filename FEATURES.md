# git-ci Canonical Features Sheet

> **Status legend:** ⏳ NOT TESTED · ✅ PASS · ❌ FAIL · 🐛 BUG · 🔧 FIXED

This is the canonical tracking document for every feature in `git-ci`. Each row is a user story derived from the source code, with the expected behaviour documented. Rows are updated as testing progresses.

---

## 1. COMMANDS (top-level)

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| C-01 | As a user, I run `gci list` to see a tree of jobs from the auto-detected CI file | Prints a tree-formatted list of jobs with stages, runner info, env vars, services, artifacts, cache, and steps | ⏳ | handler: `CmdList`; auto-detects `.github/workflows/*.yml` / `.gitlab-ci.yml` |
| C-02 | As a user, I run `gci ls` (alias) | Same as `gci list` | ⏳ | alias registered in `cli.go` |
| C-03 | As a user, I run `gci ls --format json` | Outputs pipeline as JSON to stdout | ✅ | `internal/handlers/list.go` now reads `c.String("format")` and emits `json.MarshalIndent(pipeline)`; verified `python3 json.load` succeeds |
| C-04 | As a user, I run `gci ls --format yaml` | Outputs pipeline as YAML to stdout | ✅ | same branch emits `yaml.Marshal(pipeline)` followed by a trailing newline (was missing before) |
| C-05 | As a user, I run `gci ls -f .gitlab-ci.yml` | Parses and lists GitLab pipeline | ⏳ | detectParser switches to GitlabParser |
| C-06 | As a user, I run `gci run` to execute all jobs from the auto-detected CI file | Runs every job in topological order with default bash runner | ⏳ | handler: `CmdRun` |
| C-07 | As a user, I run `gci run --dry-run` | Shows what would run without executing; should still print generated scripts in verbose mode | ⏳ | `--dry-run` / `-n` |
| C-08 | As a user, I run `gci run --job test` | Runs only the job named `test` | ⏳ | exact-name match |
| C-09 | As a user, I run `gci run --only "test-*"` | Runs all jobs whose names match the wildcard | ⏳ | `matchPattern` uses `*` substring match |
| C-10 | As a user, I run `gci run --except "deploy-*"` | Runs all jobs except those matching the wildcard | ⏳ |  |
| C-11 | As a user, I run `gci run --stage build` (GitLab) | Runs only jobs in stage `build` | ⏳ | `getJobsByStage` |
| C-12 | As a user, I run `gci run --docker` | Uses Docker runner (container) | ⏳ | requires Docker daemon |
| C-13 | As a user, I run `gci run --podman` | Uses Podman runner | ⏳ | falls back to Docker if Podman unavailable |
| C-14 | As a user, I run `gci run --parallel --max-parallel 4` | Runs independent jobs concurrently up to 4 workers | ⏳ | `groupByDependencyLevel` |
| C-15 | As a user, I run `gci run --continue-on-error` | Continues past failed jobs (does not fail pipeline) | ⏳ | `shouldSkipJob` |
| C-16 | As a user, I run `gci run --timeout 5` | Caps each job at 5 minutes | ⏳ | `--timeout` / `-t`, minutes |
| C-17 | As a user, I run `gci run --env FOO=bar --env BAZ=qux` | Adds env vars passed in the current invocation | ⏳ | `parseEnvironmentVars` |
| C-18 | As a user, I run `gci run --env-file .env` | Loads `KEY=VALUE` pairs from `.env` | ⏳ | respects comments and quotes |
| C-19 | As a user, I run `gci run --memory 1g --cpus 2` | Sets container resource limits | ⏳ | Docker runner applies to container create |
| C-20 | As a user, I run `gci run --volume /host:/container:ro` | Mounts an extra volume | ⏳ | `-V` |
| C-21 | As a user, I run `gci run --network host` | Uses host network mode | ⏳ | `--network` |
| C-22 | As a user, I run `gci run --no-cache` | Disables caching | ⏳ |  |
| C-23 | As a user, I run `gci run --pull=false` | Skips pulling Docker images (uses local cache only) | ⏳ | default is `--pull=true` |
| C-24 | As a user, I run `gci validate` to check pipeline syntax | Reports validation errors and a summary count | ⏳ | `--strict` enables more checks |
| C-25 | As a user, I run `gci validate --strict` | Performs strict checks (runner image, step empty, env var keys, artifact paths, etc.) | ⏳ |  |
| C-26 | As a user, I run `gci validate --provider github` | Forces GitHub parser for validation | ⏳ | `--provider` / `-p`, values: github / gitlab / auto |
| C-27 | As a user, I run `gci init` to scaffold a GitHub basic CI | Creates `.github/workflows/ci.yml` and prints next steps | ⏳ | `--provider` default github, `--template` default basic |
| C-28 | As a user, I run `gci init --provider gitlab --template go` | Creates `.gitlab-ci.yml` from `gitlabGoTemplate` | ⏳ | all 7 providers × 5 templates = 35 combinations |
| C-29 | As a user, I run `gci init --output my.yml --force` | Writes to `my.yml`, overwriting if needed | ⏳ | `--force` |
| C-30 | As a user, I re-run `gci init` when file exists | Errors with "file already exists. Use --force to overwrite" | ⏳ | backwards compatibility |
| C-31 | As a user, I run `gci discover` to find every CI file in the project | Walks project and prints all matches in tree/json/yaml | ⏳ | handler: `CmdDiscover` |
| C-32 | As a user, I run `gci discover --directory /tmp/proj` | Discovers inside a given directory | ⏳ |  |
| C-33 | As a user, I run `gci discover --format json` | Prints discovery result as JSON | ⏳ |  |
| C-34 | As a user, I run `gci clean --all` | Removes all git-ci tracked containers, images, and cache dirs | ⏳ | `--all` / `-a` |
| C-35 | As a user, I run `gci clean --containers` | Removes only containers with label `git-ci=true` or name prefix `git-ci` | ⏳ |  |
| C-36 | As a user, I run `gci clean --images` | Removes only images tagged with `git-ci` | ⏳ |  |
| C-37 | As a user, I run `gci clean --cache` | Removes `.git-ci-cache`, `.git-ci`, `tmp/git-ci`, `$HOME/.cache/git-ci`, `$HOME/.git-ci` | ⏳ |  |
| C-38 | As a user, I run `gci clean --containers` (without --force) | Prompts y/N before each removal | ⏳ | interactive, hard to script — may need --force for tests |
| C-39 | As a user, I run `gci env list` | Prints env vars whose names start with `GIT_CI_` or `CI` | ✅ | `CmdEnvList` filter: `verbose \|\| strings.HasPrefix(env, "GIT_CI_") \|\| strings.HasPrefix(env, "CI")`; regression: `TestCmdEnvList_NonVerboseHidesUnrelatedVars` |
| C-40 | As a user, I run `gci env list --verbose` | Prints **all** env vars (filter lifted) | ✅ | real-app regression: `cmd/cli_test.go::TestCliApp_EnvListVerbose_NoUnknownFlag` proves urfave/cli accepts the flag; handler-level: `TestCmdEnvList_VerboseIncludesUnfilteredVars` |
| C-41 | As a user, I run `gci env set FOO=bar BAZ=qux` | Sets both vars in the current process | ⏳ | hard to verify in a shell since exec resets env |
| C-42 | As a user, I run `gci env set FOO=bar --save --file my.env` | Persists to `my.env` (merged with existing entries) | ✅ | `saveEnvFile`; real-app regressions drive `cli.App.Run(...)` so urfave/cli quirks are covered (`TestCliApp_EnvSetSaveAfterPositional_PersistsFile` and `..._SaveBeforePositional_PersistsFile`) |
| C-43 | As a user, I run `gci env load --file my.env` | Loads entries from file and sets them in process | ⏳ |  |
| C-44 | As a user, I run `gci env load` when file missing | Errors with "environment file not found: .env" | ⏳ |  |
| C-45 | As a user, I run `gci config show` | Prints current `.git-ci.yml` (or "no config found") | ⏳ | looks for several config paths |
| C-46 | As a user, I run `gci config init` | Creates `.git-ci.yml` with default configuration | ⏳ |  |
| C-47 | As a user, I run `gci config init --output custom.yml --force` | Overwrites `custom.yml` with defaults | ⏳ |  |

---

## 2. GLOBAL FLAGS

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| G-01 | As a user, I run `gci --verbose run` | Verbose runner output (commands, parsed pipeline, env) | ⏳ | global `--verbose` / `GIT_CI_VERBOSE` |
| G-02 | As a user, I run `gci --debug run` | Even more verbose | ⏳ | resolved identically to verbose in handler |
| G-03 | As a user, I run `gci --quiet run` | Suppresses runner output | ✅ | `RunnerConfig.Quiet` plumbed through bash/podman constructors; formatter Print* methods + `Progress` respect quiet; PrintError/PrintStepFailed intentionally bypass; real regressions in `internal/runners/quiet_test.go` (formatter silence + propagation tests) |
| G-04 | As a user, I run `gci --workdir /tmp run` | Sets working directory before parsing & running | ⏳ | resolves to absolute path |
| G-05 | As a user, I run `gci --workdir /does/not/exist run` | Errors with "workdir does not exist: ..." | ⏳ | validation in `getWorkdir` |
| G-06 | As a user, I run `gci --config my.yml run` | Loads my.yml first | ⏳ | `LoadConfigWithDefaults` |
| G-07 | As a user, I run `gci --help` | Shows app help | ⏳ | urfave/cli built-in |
| G-08 | As a user, I run `gci --version` | Prints formatted version (Commit[:7] + [Branch]) | ⏳ | `formatVersion` |
| G-09 | As a user, I run `git config --global alias.ci '!gci'` then `git ci run` | Alias works via shebang `!/usr/bin/env gci` | ⏳ | documented in README |

---

## 3. PARSERS

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| P-01 | As a user, I provide `.github/workflows/ci.yml` | GitHub parser is selected by path | ⏳ | `detectParser` |
| P-02 | As a user, I provide `.gitlab-ci.yml` | GitLab parser is selected | ⏳ |  |
| P-03 | As a user, I provide `.circleci/config.yml` | CircleCI parser selected | ⏳ |  |
| P-04 | As a user, I provide `.drone.yml` | Drone parser selected | ⏳ |  |
| P-05 | As a user, I provide `.travis.yml` | Travis parser selected | ⏳ |  |
| P-06 | As a user, I provide a file with `version: 2.1` + `jobs:` | Content-based detection picks CircleCI | ⏳ |  |
| P-07 | As a user, I provide content with `kind: pipeline` | Content picks Drone | ⏳ |  |
| P-08 | As a user, I provide content with `language:` + `script:` (no `on:`) | Content picks Travis | ⏳ |  |
| P-09 | As a user, I provide content with `on:` + `runs-on:` | Content picks GitHub | ⏳ |  |
| P-10 | As a user, I provide content with `stages:` or `script:` | Content picks GitLab | ⏳ |  |
| P-11 | As a user, I provide a file with `bitbucket-` or `azure-` in name | Falls back to GitHub parser (README admits) | ⏳ |  |
| P-12 | As a user, I provide a file with no recognizable content | Falls back to GitHub parser with a stderr warning | ⏳ | generatePipelineTemplate returns GitHub basic |

---

## 4. RUNNERS

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| R-01 | Bash runner: runs a single bash job | Each step is piped to bash, ok/fail tracked | ⏳ |  |
| R-02 | Bash runner: when step uses `actions/checkout@v3` | Runs `git fetch --all --tags` (no-op outside git repo) | ⏳ | `runActionStep` |
| R-03 | Bash runner: when step uses `actions/setup-go@v5` | Verifies `go version` is installed | ⏳ |  |
| R-04 | Bash runner: when step uses unsupported `uses:` | Skips with a warning ("Unsupported action: ...") | ⏳ |  |
| R-05 | Bash runner: continues on error per step | Step keeps going in `executeWithRetry`-like split path | ⏳ | `ContinueOnErr` |
| R-06 | Bash runner: stream command output in real time | Two goroutines copy stdout/stderr | ⏳ | `streamOutput` |
| R-07 | Bash runner: timeout per job | Stops after configured minutes | ⏳ |  |
| R-08 | Docker runner: pulls image unless `--pull=false` | Uses catthehacker images for Ubuntu runners | ⏳ |  |
| R-09 | Docker runner: maps everything to bash via `/bin/bash -c SCRIPT` | Mounts workdir at `/workspace` | ⏳ |  |
| R-10 | Docker runner: detects Alpine image and switches shell | `containerShell()` returns `/bin/sh` for alpine | ⏳ |  |
| R-11 | Docker runner: service containers share one bridge network with DNS alias = service name | DNS resolves `postgres` → service container | ⏳ | tested via integration tests? |
| R-12 | Podman runner: uses --pod for service container sharing | Falls back to docker bridge network if docker | ⏳ |  |
| R-13 | Runner: honours `--memory` and `--cpus` flags | `parseMemoryString`, `parseCPUString` | ⏳ |  |
| R-14 | Runner: cleans up containers after job | `Cleanup()` removes all tracked IDs | ⏳ |  |

---

## 5. MATRIX & DEPENDENCY ENGINE

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| M-01 | As a user, I declare a 2x2 matrix in a job | Produces 4 jobs named "job (v1,v2)" etc. | ⏳ | `expandMatrixJobs` |
| M-02 | As a user, I declare `include:` | Includes whose keys overlap are merged into existing combos; non-overlapping become new combos | ⏳ |  |
| M-03 | As a user, I declare `exclude:` | Matching combinations are removed | ⏳ |  |
| M-04 | As a user, downstream job depends on the pre-expansion matrix job name | Needs resolves to all expanded variant names | ⏳ | post-pass in `expandMatrixJobs` |
| M-05 | As a user, I have `lint → test → build → deploy` chain | Topo sort returns `[lint, test, build, deploy]` | ⏳ | Kahn's algorithm |
| M-06 | As a user, I have circular dependency (`a → b → a`) | `run` errors "circular dependency detected" | ⏳ |  |
| M-07 | As a user, I run a parallel pipeline with `parallel` flag | Jobs are grouped by dependency level | ⏳ |  |
| M-08 | Matrix values are passed as env vars `OS` + `MATRIX_OS` | Both raw and normalized env keys are set | ⏳ |  |

---

## 6. ENVIRONMENT & CONFIG

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| E-01 | As a user, I have a `CI=true`, `GIT_CI=true`, `GIT_CI_VERSION=x` set automatically | `setupEnvironment()` injects these unless user overrides | ⏳ |  |
| E-02 | As a user, I set `GIT_CI_FILE=.github/workflows/main.yml` and run `gci run` | Pipelines runs against that file even from anywhere | ⏳ |  |
| E-03 | As a user, I have a `.git-ci.yml` with `defaults.runner: docker` | `run` auto-selects Docker when no runner flag | ⏳ | `applyConfigToContext` |
| E-04 | As a user, I have `.git-ci.yml` with `defaults.parallel: true` | Parallel mode engaged automatically | ⏳ |  |
| E-05 | As a user, I have `.git-ci.yml` with `docker.volumes: [./c:/cache]` | Volume mounted automatically | ⏳ |  |
| E-06 | `.git-ci.yml` with `environment: { MY_VAR: hello }` | Injected when not already in env | ⏳ |  |

---

## 7. ERROR HANDLING

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| Z-01 | As a user, I run `gci run` from a directory with no CI file | Errors with "no CI configuration file found. Use -f to specify file" | ⏳ |  |
| Z-02 | As a user, I pass a malformed YAML | Parser returns descriptive error including file name | ⏳ |  |
| Z-03 | As a user, I pass a YAML with no `jobs:` key | Errors with "no jobs defined in workflow" | ⏳ |  |
| Z-04 | As a user, I pass a YAML with `needs: [nonexistent]` | Errors with "job X depends on non-existent job Y" | ⏳ |  |
| Z-05 | As a user, I pass a stage that doesn't exist | Errors with "job X references undefined stage Y" | ⏳ |  |
| Z-06 | Bash runner: command exits non-zero | Pipeline marked failed; non `--continue-on-error` returns nonzero | ⏳ |  |

---

## 8. OUTPUT FORMATTING

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| O-01 | Verbose mode prints commands, env, generated script | Used by Docker runner to dump container config | ⏳ |  |
| O-02 | Dry-run shows "DRY RUN MODE - Commands will be displayed but not executed" | Banner printed by PrintDryRun | ⏳ |  |
| O-03 | Job completed shows "✓ Job 'name' completed successfully" | Uses green ✓ | ⏳ |  |
| O-04 | Step completed shows "Step completed in 0.1s" | Uses ✓ | ⏳ |  |
| O-05 | Step failure shows "✗ Step FAILED after 0.1s: <reason>" | Red X | ⏳ |  |
| O-06 | Skipped step shows "○ Step skipped: condition not met" | Yellow ○ | ⏳ |  |

---

## 9. CMDLINE EDGE CASES

| ID | User Story | Expected Behaviour | Status | Notes |
|---|---|---|---|---|
| X-01 | As a user, I run `gci list -f i-do-not-exist.yml` | Errors clearly | ⏳ |  |
| X-02 | As a user, I run `gci run --only "nope-*"` when no matches | "Warning: job 'nope-*' not found" | ⏳ |  |
| X-03 | As a user, I run `gci env set` with no args | "no environment variables specified" | ⏳ |  |
| X-04 | As a user, I run `gci env set NOEQUALSIGN` | "invalid format: NOEQUALSIGN" | ⏳ |  |
| X-05 | No CLI args: `gci` | Prints help | ⏳ | urfave/cli default |
| X-06 | Unknown subcommand: `gci banana` | Prints help / "unknown command" | ⏳ | urfave/cli default |
| X-07 | As a user, I pass `--pull=true` / `--pull=false` | Equivalent to `--pull` and `--no-pull` style — should be tolerated | ⏳ | flags registered with default `true` — env handling is sensitive (`EnvVars: ["GIT_CI_PULL"]`) |

---

## TEST RESULTS — POST-FIX RE-RUN

All four confirmed bugs verified post-fix on a freshly rebuilt `build/gci` (`go build -o build/gci ./cmd`):

| Bug | Repro command | Status | Verified outcome |
|---|---|---|---|
| #1 list `--format json` | `gci list --format json -f .github/workflows/ci.yml \| python3 -c 'json.load(sys.stdin)'` | ✅ | Valid JSON; `len(jobs)==N` |
| #1 list `--format yaml` | `gci list --format yaml -f .gitlab-ci.yml` | ✅ | Stream now ends with `\n` |
| #2 env list `--verbose` | `gci env list --verbose` | ✅ | No crash; header + content shown |
| #3 env set with flags after arg | `gci env set TV=hello --save --file r3.env` | ✅ | file written with `TV=hello` |
| #3 bwc (no flags) | `gci env set SIMPLE=z` | ✅ | prints `✓ Set SIMPLE=z` and exits 0 |
| #3 ambiguous ordering | `gci env set --save TV2=world --file r3b.env` | ✅ | file written |
| #4 `--quiet` | `gci --quiet run --dry-run -f ...` | ✅ | output drops from 75 → 0 (or to error lines only); direct `fmt.Printf` / `io.Copy(os.Stdout, ...)` callers in podman/docker `dryRunJob` & `streamLogs` now go through the package-level quiet redirect |

### Remaining gaps (documented, not fixed — pre-regression-suite)

- Direct subprocess output via OS fd 1/2 inheritance (processes that shell out without setting `cmd.Stdout = io.Discard`) still reaches the host terminal even under `--quiet`. Podman/Docker pull in verbose mode is the most visible case; non-verbose pull already uses `-q`.
- `Progress.quiet` is captured at construction time. If any caller toggles `OutputFormatter.Quiet` mid-pipeline, in-flight `Progress` objects keep their stale value.
- `gci env set --save` (no `KEY=VALUE`) still succeeds silently without writing a file when no env vars are present. (We preserved the explicit error for the “no args” case.)

## REGRESSION SUITE

The four bugs above are now locked in by Go unit tests. **All 23 tests pass:**

```
go test -count=1 -run 'CmdList_Format|CmdEnvList_|CmdEnvSet_|OutputFormatter_Quiet|OutputFormatter_NotQuiet|OutputFormatter_SetQuietRoundTrip|NewBashRunner_|TestCliApp_' ./internal/handlers/... ./internal/runners/... ./cmd/...
```

| Bug | Test files | What regresses |
|---|---|---|
| #1 | `internal/handlers/list_format_test.go` + `cmd/cli_test.go` | drives real `cli.App.Run(...)` and parses the output as JSON/YAML; fixture in `t.TempDir()` |
| #2 | `internal/handlers/env_test.go` + `cmd/cli_test.go` | handler-level sanity + real-app trip; the app-level test catches a regression in `commands()` (BoolFlag missing on env list) without relying on stdlib flag's behaviour |
| #3 | `internal/handlers/env_test.go` + `cmd/cli_test.go` | handler-level sanity (filter to KEY=VAL args) + real-app trip that exercises urfave/cli's flag-after-positional quirk (stdlib `flag.FlagSet` does not reproduce it) |
| #4 | `internal/runners/quiet_test.go` | formatter silence for every Print method (PrintError/PrintStepFailed intentionally bypass); + `TestNewBashRunner_PropagatesQuiet` / `_PropagatesVerbose` / `_DefaultsAreQuietFalse` guarding the constructor wiring |
| #5 | `internal/runners/quiet_redirect_test.go` | helper unit tests (acquire/release round-trip, ref-count math, defensive releases, `fmt.Printf` swallowing) + runner integration tests (`BashRunner.RunJob` + `PodmanRunner.dryRunJob` under Quiet → zero bytes on host stdout, parity checks under non-Quiet) |

### Remaining gaps (post-regression-suite)

- `DockerRunner`'s `streamLogs` (Docker SDK `stdcopy.StdCopy(os.Stdout, os.Stderr, reader)`) and `createContainer` verbose branches (`fmt.Println(script)`, etc.) can only be exercised end-to-end in an integration environment with a working Docker daemon. The helper-level test proves the redirect swallows `fmt.Printf`; the runner-level wiring mirrors `PodmanRunner`'s structure (constructor acquires, `Cleanup` releases), but actual-egress is documented, not unit-tested.
- `NewPodmanRunner` and `NewDockerRunner` shell out to `findContainerRuntime()` / `client.Ping()` in their constructors. Wiring coverage for these constructors is by structural inspection (same call pattern as `NewBashRunner`).
- `TestCmdEnvSet_NoArgsStillErrors` does an exact string match on `err.Error()`. Future wording changes will trip it for cosmetic reasons.
- `captureStdout*` test helpers are synchronous-only by happy accident; async callers (e.g. running `BashRunner.RunJob`) would silently lose output past `w.Close()`.
