# Example CI/CD Pipeline Files

This directory contains realistic pipeline configuration files that demonstrate
the features of **git-ci** across multiple CI providers. Use them to explore,
test, and learn.

## Directory Structure

```
examples/
├── github/               # GitHub Actions workflows
│   ├── simple.yml        # Minimal build & test (cross-provider)
│   ├── go-ci.yml         # Full Go CI: lint + matrix test + build
│   ├── matrix.yml        # Multi-dimensional matrix with include/exclude
│   ├── services.yml      # Integration tests with PostgreSQL, Redis, MinIO
│   ├── full-stack.yml    # Combined: matrix + services + parallel
│   └── templates/        # Auto-generated from `gci init` templates
│       ├── basic.yml
│       ├── go.yml
│       ├── node.yml
│       ├── python.yml
│       └── docker.yml
├── gitlab/               # GitLab CI pipelines
│   ├── simple.yml        # Minimal build & test (cross-provider)
│   ├── multi-stage.yml   # Full pipeline: lint → test → build → deploy
│   └── templates/        # Auto-generated from `gci init` templates
│       ├── basic.yml
│       ├── go.yml
│       ├── node.yml
│       ├── python.yml
│       └── docker.yml
├── circleci/
│   ├── config.yml        # CircleCI pipeline with orbs, executors, workflows
│   ├── go-ci.yml         # CircleCI Go CI: lint + parallelism + build + deploy
│   ├── matrix.yml        # Parameterized jobs demonstrating matrix expansion
│   ├── services.yml      # Integration tests with PostgreSQL, Redis, MinIO
│   ├── full-stack.yml    # Combined: matrix + services + parallel + deploy
│   └── templates/        # Auto-generated from `gci init` templates
├── drone/
│   ├── .drone.yml        # Drone CI pipeline with services and triggers
│   ├── go-ci.yml         # Drone CI Go CI: lint + test matrix + build + deploy
│   ├── matrix.yml        # Multi-node matrix across Node versions
│   ├── services.yml      # Integration tests with PostgreSQL, Redis, MinIO
│   ├── full-stack.yml    # Combined: matrix + services + parallel + deploy
│   └── templates/        # Auto-generated from `gci init` templates
├── travis/
│   ├── .travis.yml       # Travis CI pipeline with version matrix and stages
│   ├── go-ci.yml         # Travis CI Go CI: matrix test + lint + build + deploy
│   ├── matrix.yml        # Multi-dimensional matrix with include/exclude
│   ├── services.yml      # Integration tests with PostgreSQL, Redis
│   ├── full-stack.yml    # Combined: matrix + services + deploy
│   └── templates/        # Auto-generated from `gci init` templates
├── bitbucket/
│   └── templates/
│       └── basic.yml     # Auto-generated Bitbucket Pipelines template
├── azure/
│   └── templates/
│       └── basic.yml     # Auto-generated Azure Pipelines template
└── config/
    └── git-ci.yml        # Full .git-ci.yml configuration reference
```

## Quick Start

```bash
# List jobs in any example
gci ls -f examples/github/go-ci.yml

# Validate a pipeline
gci validate -f examples/github/go-ci.yml
gci validate -f examples/gitlab/multi-stage.yml --provider gitlab

# Run a pipeline with the bash runner (no Docker needed)
gci run -f examples/github/simple.yml

# Run with Docker runner
gci run -f examples/github/services.yml --docker

# Run a specific job
gci run -f examples/github/matrix.yml --job "test (ubuntu-latest, 20)"

# Run in parallel
gci run -f examples/github/full-stack.yml --docker --parallel

# Dry-run to preview execution
gci run -f examples/github/go-ci.yml --dry-run --verbose

# Discover all examples (from project root)
gci discover --directory examples
```

## Cross-Provider Comparison

The `github/simple.yml` and `gitlab/simple.yml` files define the same pipeline
in GitHub Actions and GitLab CI syntax. Both produce identical behavior when
run with git-ci:

```bash
gci run -f examples/github/simple.yml
gci run -f examples/gitlab/simple.yml
```

The `circleci/config.yml`, `drone/.drone.yml`, and `travis/.travis.yml` files
show how the same Go CI pipeline is expressed in three other popular providers.
These use their native syntax and features (orbs, pipelines, stages).

## Provider Syntax Comparison

| Feature | GitHub | GitLab | CircleCI | Drone | Travis |
|---|---|---|---|---|---|---|
| Runner spec | `runs-on:` | `image:` / `tags:` | `executor:` | `type: docker` | `language:` |
| Steps | `steps:` array | `script:` list | `steps:` / orbs | `steps:` array | `script:` / lifecycle hooks |
| Dependencies | `needs:` | `needs:` | `requires:` in workflow | `depends_on:` | `stages:` / `jobs.include` |
| Matrix | `strategy.matrix` | `parallel:matrix` | `parameters:` / parallelism | `matrix:` (plugin) | version list / `jobs.include` |
| Services | `services:` | `services:` | Orb helpers | `services:` block | `services:` |
| Artifacts | `upload-artifact` action | `artifacts:` | `store_artifacts` / workspace | `s3:` plugin | `deploy:` / `before_deploy` |
| Env vars | `env:` | `variables:` | `environment:` | `environment:` | `env:` global |
| Conditions | `if:` | `rules:` / `only:`/`except:` | `filters:` in workflow | `when:` / `trigger:` | `on:` in deploy / stages |

## Run All Examples

A test script is provided to validate and dry-run every example:

```bash
bash scripts/run-examples.sh
```

## Test Status

Status of each provider's examples when run through `scripts/run-examples.sh`.
The test suite runs `gci validate`, `gci ls`, and `gci run --dry-run --verbose`
on every example file.

| Provider | Validates | Lists Jobs | Dry-Run | Parser | Templates |
|---|---|---|---|---|---|
| ✅ GitHub Actions | ✅ Pass | ✅ Pass | ✅ Pass | Dedicated (`parsers/github.go`) | ✅ basic, go, node, python, docker |
| ✅ GitLab CI | ✅ Pass | ✅ Pass | ✅ Pass | Dedicated (`parsers/gitlab.go`) | ✅ basic, go, node, python, docker |
| ✅ CircleCI | ✅ Pass | ✅ Pass | ✅ Pass | Dedicated (`parsers/circleci.go`) | ✅ basic, go, node, python, docker |
| ✅ Drone CI | ✅ Pass | ✅ Pass | ✅ Pass | Dedicated (`parsers/drone.go`) | ✅ basic, go, node, python, docker |
| ✅ Travis CI | ✅ Pass | ✅ Pass | ✅ Pass | Dedicated (`parsers/travis.go`) | ✅ basic, go, node, python, docker |

Providers with ❌ status use the native syntax in their example files (orbs,
pipelines, stages, etc.) but don't have a dedicated parser yet — they fall back
to the GitHub Actions parser which cannot interpret their provider-specific
structures. Adding a dedicated parser for a provider automatically enables full
support for its examples.

```bash
# Check current status yourself
bash scripts/run-examples.sh
```

## Template-Generated Files

The `templates/` subdirectories contain pipeline files auto-generated from the
template definitions in `internal/handlers/init.go`. These are created by:

```bash
bash scripts/sync-templates.sh
```

After modifying templates in `init.go`, run the sync script and commit the
regenerated files. A CI check (`scripts/sync-templates.sh --check`) verifies
they stay in sync on every pull request.

Each template showcases a specific language or framework:

| Template | Language | Description |
|---|---|---|
| `basic` | Any | Minimal pipeline with test + build jobs |
| `go` | Go | `go test -race`, `go vet`, `golangci-lint`, `go build` |
| `node` | Node.js | Matrix by Node version, `npm ci`, `npm test`, `npm run build` |
| `python` | Python | Matrix by Python version, pytest with coverage, flake8 lint |
| `docker` | Docker | Docker build with Buildx, registry login, multi-stage push |

| Template File | Provider | Matrix | Services | Deps | Artifacts | Parallel |
|---|---|---|---|---|---|---|
| `github/templates/basic.yml` | GitHub | — | — | ✅ | — | — |
| `github/templates/go.yml` | GitHub | — | — | ✅ | — | — |
| `github/templates/node.yml` | GitHub | ✅ | — | ✅ | ✅ | — |
| `github/templates/python.yml` | GitHub | ✅ | — | ✅ | — | — |
| `github/templates/docker.yml` | GitHub | — | — | — | ✅ | — |
| `gitlab/templates/basic.yml` | GitLab | — | — | ✅ | — | — |
| `gitlab/templates/go.yml` | GitLab | — | — | ✅ | ✅ | — |
| `gitlab/templates/node.yml` | GitLab | — | — | ✅ | ✅ | — |
| `gitlab/templates/python.yml` | GitLab | — | — | ✅ | ✅ | — |
| `gitlab/templates/docker.yml` | GitLab | — | ✅ | — | ✅ | — |
| `circleci/templates/basic.yml` | CircleCI | — | — | ✅ | — | ✅ |
| `circleci/templates/go.yml` | CircleCI | — | — | ✅ | ✅ | ✅ |
| `circleci/templates/node.yml` | CircleCI | — | — | ✅ | — | ✅ |
| `circleci/templates/python.yml` | CircleCI | — | — | ✅ | — | ✅ |
| `circleci/templates/docker.yml` | CircleCI | — | — | 🟡 | — | ✅ |
| `drone/templates/basic.yml` | Drone | — | — | ✅ | — | — |
| `drone/templates/go.yml` | Drone | — | — | ✅ | — | — |
| `drone/templates/node.yml` | Drone | — | — | ✅ | — | — |
| `drone/templates/python.yml` | Drone | — | — | ✅ | — | — |
| `drone/templates/docker.yml` | Drone | — | — | 🟡 | — | — |
| `travis/templates/basic.yml` | Travis | — | — | ✅ | — | — |
| `travis/templates/go.yml` | Travis | ✅ | — | ✅ | ✅ | — |
| `travis/templates/node.yml` | Travis | ✅ | — | ✅ | — | — |
| `travis/templates/python.yml` | Travis | ✅ | — | ✅ | — | — |
| `travis/templates/docker.yml` | Travis | — | ✅ | — | 🟡 | — |
| `bitbucket/templates/basic.yml` | Bitbucket | — | — | — | — | — |
| `azure/templates/basic.yml` | Azure | — | — | ✅ | — | ✅ |

## Hand-Crafted Examples

Beyond the auto-generated templates, hand-crafted examples demonstrate more
realistic, multi-stage workflows that combine several features at once.

| Example File | Provider | Matrix | Services | Deps | Artifacts | Parallel |
|---|---|---|---|---|---|---|
| `github/simple.yml` | GitHub | — | — | ✅ | — | — |
| `github/go-ci.yml` | GitHub | ✅ | — | ✅ | ✅ | — |
| `github/matrix.yml` | GitHub | ✅ | — | ✅ | ✅ | — |
| `github/services.yml` | GitHub | — | ✅ | ✅ | — | — |
| `github/full-stack.yml` | GitHub | ✅ | ✅ | ✅ | ✅ | ✅ |
| `gitlab/simple.yml` | GitLab | — | — | ✅ | — | — |
| `gitlab/multi-stage.yml` | GitLab | — | ✅ | ✅ | ✅ | ✅ |
| `circleci/config.yml` | CircleCI | — | — | ✅ | — | ✅ |
| `circleci/go-ci.yml` | CircleCI | — | — | ✅ | ✅ | — |
| `circleci/matrix.yml` | CircleCI | ✅ | — | ✅ | — | ✅ |
| `circleci/services.yml` | CircleCI | — | ✅ | ✅ | — | — |
| `circleci/full-stack.yml` | CircleCI | ✅ | ✅ | ✅ | ✅ | ✅ |
| `drone/.drone.yml` | Drone | — | ✅ | ✅ | — | — |
| `drone/go-ci.yml` | Drone | — | ✅ | ✅ | — | — |
| `drone/matrix.yml` | Drone | ✅ | — | ✅ | — | ✅ |
| `drone/services.yml` | Drone | — | ✅ | ✅ | — | — |
| `drone/full-stack.yml` | Drone | ✅ | ✅ | ✅ | — | ✅ |
| `travis/.travis.yml` | Travis | ✅ | — | ✅ | ✅ | — |
| `travis/go-ci.yml` | Travis | ✅ | — | ✅ | ✅ | — |
| `travis/matrix.yml` | Travis | ✅ | — | ✅ | — | — |
| `travis/services.yml` | Travis | — | ✅ | ✅ | — | — |
| `travis/full-stack.yml` | Travis | ✅ | ✅ | ✅ | ✅ | ✅ |
