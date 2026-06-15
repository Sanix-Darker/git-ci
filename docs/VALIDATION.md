# Real-World Validation Plan (Docker available)

Goal: exercise **every feature and every provider** against real, barebones
pipelines using actual execution (Docker + bash), inspect output/logs on
failure, fix, and loop until each item passes.

Environment: Docker 29.5.2 running. Go 1.25 at `/usr/local/go/bin`. Binary at
`/tmp/gci`. Test fixtures live under `.tmp-validation/` (gitignored, cleaned up
at the end). All file writes stay inside the workspace.

Legend per check: PASS / FAIL(fixed) / BLOCKED.

## Conventions
- Global flags (`--verbose`, `--workdir`) come BEFORE the subcommand.
- Each scenario gets its own subdir under `.tmp-validation/<name>/` so jobs run
  in isolation and artifacts/caches don't collide.
- For Docker runs we prefer small images (`alpine`, `busybox`, `node:alpine`)
  to keep pulls fast and deterministic.

---

## Group A — Core commands (no execution)
| ID | Check | Command |
|----|-------|---------|
| A1 | version | `gci --version` |
| A2 | help | `gci --help` |
| A3 | ls tree (github) | `gci ls -f <gh>` |
| A4 | ls json valid | `gci ls -f <gh> --format json \| jq` |
| A5 | ls yaml valid | `gci ls -f <gh> --format yaml` |
| A6 | validate ok | `gci validate -f <gh>` |
| A7 | validate strict | `gci validate -f <gh> --strict` |
| A8 | discover | `gci discover` | ✅ Implemented |
| A9 | discover json | `gci discover --format json \| jq` | ✅ Implemented |
| A10 | config show/init | `gci config show`, `gci config init` |
| A11 | env list/set/load | `gci env ...` |
| A12 | init templates | `gci init -p github -t go -o ...` |

## Group B — Bash runner (native execution)
| ID | Check |
|----|-------|
| B1 | single job, single step echo |
| B2 | multi-step job, ordered output |
| B3 | env var expansion `$VAR` and `${{ env.X }}` |
| B4 | matrix expansion runs N legs |
| B5 | needs ordering (sequential) |
| B6 | parallel execution (`--parallel`) |
| B7 | continue-on-error |
| B8 | failing step -> exit 1 |
| B9 | secret masking in output |
| B10 | artifacts between jobs |
| B11 | cache save/restore across runs |
| B12 | `--format json` run output |
| B13 | `if:` condition skips step |
| B14 | dry-run prints plan, executes nothing |

## Group C — Docker runner (real containers)
| ID | Check |
|----|-------|
| C1 | simple job in alpine echoes |
| C2 | image resolution from runs-on (ubuntu->catthehacker) |
| C3 | explicit `image:`/container runs |
| C4 | multi-step script in one container |
| C5 | `${{ }}` + matrix env inside container |
| C6 | services: postgres reachable by DNS alias |
| C7 | resource limits `--memory/--cpus` accepted |
| C8 | failing container -> exit non-zero surfaced |
| C9 | cleanup removes containers/networks |
| C10 | secret masking in container logs |
| C11 | `clean --containers` removes git-ci containers |

## Group D — Podman runner
| ID | Check |
|----|-------|
| D1 | `--runtime podman` without podman -> clear error (no masquerade) |
| D2 | `--runtime auto` selects docker when podman absent |

## Group E — Providers (parse + ls + dry-run + real bash run where simple)
Each provider gets a barebones pipeline; verify ls, validate, and a real run.
| ID | Provider | File | Status |
|----|----------|------|--------|
| E1 | GitHub Actions | `.github/workflows/*.yml` | ✅ Implemented |
| E2 | GitLab CI | `.gitlab-ci.yml` | ✅ Implemented |
| E3 | Jenkins | `Jenkinsfile` | ❌ Not yet implemented — falls back to GitHub parser |
| E4 | CircleCI | `.circleci/config.yml` | ❌ Not yet implemented — falls back to GitHub parser |
| E5 | Azure | `azure-pipelines.yml` | ❌ Not yet implemented — falls back to GitHub parser |
| E6 | Bitbucket | `bitbucket-pipelines.yml` | ❌ Not yet implemented — falls back to GitHub parser |
| E7 | Drone | `.drone.yml` | ❌ Not yet implemented |
| E8 | Travis | `.travis.yml` | ❌ Not yet implemented |
| E9 | Tekton | `tekton.yaml` | ❌ Not yet implemented |
| E10 | Argo | `argo-workflow.yaml` | ❌ Not yet implemented |

## Group F — Docker provider runs (real containers, per provider)
Run a barebones job for each provider through the Docker runner with a small
image and assert the expected output line appears.
| ID | Provider | runtime |
|----|----------|---------|
| F1 | GitHub (container) | docker |
| F2 | GitLab (image: alpine) | docker |
| F3 | CircleCI (docker image) | docker | ❌ Not yet implemented |
| F4 | Drone (image) | docker | ❌ Not yet implemented |

## Group G — Filtering & selection
| ID | Check |
|----|-------|
| G1 | `--job` selects one |
| G2 | `--only`/`--except` wildcards |
| G3 | `--stage` (gitlab) |

## Exit-code matrix
| Scenario | Expected |
|----------|----------|
| success | 0 |
| runtime failure | 1 |
| usage error (bad flag/job) | 2 |
| JSON-mode failure | 1, clean JSON on stdout |

## Loop protocol
For each FAIL: capture stdout+stderr to a log, read it, identify root cause,
fix code, rebuild, re-run the specific check, then re-run the affected group.
Record the outcome inline in this file's results section (appended at the end).
