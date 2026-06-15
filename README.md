# git-ci

![asset](./asset.png)

Run CI/CD pipelines locally with native Go support.

**⚠️NOTE:** This project is still under development, no stable release so far.

## FEATURES

- Local execution of CI/CD workflows
- Multiple runner support (Bash, Docker, Podman)
- Parser support for GitHub Actions and GitLab CI
- Matrix strategy expansion (Cartesian product with include/exclude)
- Topological dependency resolution (Kahn's algorithm)
- Docker service containers with bridge networks and DNS aliases
- Parallel job execution with configurable concurrency
- Resource limits (memory, CPUs) for containerized jobs
- Content-based provider auto-detection
- Strict validation mode
- Environment management
- Pipeline validation and dry-run mode
- Job filtering with `--only` / `--except` (wildcard support)

## SUPPORTED PLATFORMS

### PARSERS

- ✅ GitHub Actions
- ✅ GitLab CI
- ❌ Azure DevOps (detected via file name, parsed as GitHub)
- ❌ Bitbucket Pipelines (detected via file name, parsed as GitHub)
- ❌ CircleCI
- ❌ Jenkins, Drone, Travis, Tekton, Argo

### RUNNERS

- ✅ Bash (native shell execution)
- ✅ Docker (containerized execution)
- ✅ Podman (rootless containers)
- ❌ Kubernetes (cloud-native execution)
- ❌ AWS Lambda (serverless)
- ❌ Firecracker (microVMs)

## REQUIREMENTS

- Go 1.24+ (for building from source)
- Docker/Podman (optional, for containerized execution)

## INSTALLATION

```bash
# from source
go install github.com/sanix-darker/git-ci/cmd@latest

# or, from a local clone
make install

# download binary
curl -L https://github.com/sanix-darker/git-ci/releases/latest/download/gci-$(uname -s)-$(uname -m) -o gci
chmod +x gci
sudo mv gci /usr/local/bin/

# create git alias
git config --global alias.ci '!gci'
```

## QUICK START

```bash
# list jobs from detected workflow
git ci ls

# run all jobs
git ci run

# run specific job
git ci run --job test

# run with Docker
git ci run --docker

# run with Podman
git ci run --podman

# dry run (simulate execution)
git ci run --dry-run

# parallel execution
git ci run --parallel --max-parallel 4

# validate pipeline
git ci validate

# strict validation
git ci validate --strict

# initialize new pipeline
git ci init --provider github --template go

# filter jobs
git ci run --only "test-*" --except "test-integration"

# resource limits
git ci run --docker --memory 2g --cpus 2
```

## CLI REFERENCE

### GLOBAL OPTIONS

```
--verbose            Enable verbose output            [$GIT_CI_VERBOSE]
--debug              Enable debug mode                [$GIT_CI_DEBUG]
--quiet, -q          Suppress output                  [$GIT_CI_QUIET]
--config, -c VALUE   Config file path                 [$GIT_CI_CONFIG]
--workdir, -w VALUE  Working directory (default: ".")  [$GIT_CI_WORKDIR]
--version            Print version
--help, -h           Show help
```

### list (alias: ls)

List jobs and pipelines.

```
--file, -f VALUE   Pipeline file path          [$GIT_CI_FILE]
--format VALUE     Output format: tree, json, yaml (default: "tree")
```

```bash
git ci ls
git ci ls -f .github/workflows/ci.yml
git ci ls --format json
```

### run (aliases: r, exec)

Run jobs or pipelines.

```
--file, -f VALUE          Pipeline file path                    [$GIT_CI_FILE]
--job, -j VALUE           Job name to run                       [$GIT_CI_JOB]
--stage, -s VALUE         Stage name to run                     [$GIT_CI_STAGE]
--only VALUE              Run only these jobs (repeatable)      [$GIT_CI_ONLY]
--except VALUE            Run all jobs except these (repeatable) [$GIT_CI_EXCEPT]
--docker, -d              Use Docker runner                     [$GIT_CI_DOCKER]
--podman                  Use Podman runner                     [$GIT_CI_PODMAN]
--dry-run, -n             Perform a dry run                     [$GIT_CI_DRY_RUN]
--parallel, -p            Run jobs in parallel                  [$GIT_CI_PARALLEL]
--max-parallel VALUE      Maximum parallel jobs (default: NumCPU) [$GIT_CI_MAX_PARALLEL]
--continue-on-error       Continue running on error             [$GIT_CI_CONTINUE_ON_ERROR]
--timeout, -t VALUE       Job timeout in minutes (default: 30)  [$GIT_CI_TIMEOUT]
--env, -e VALUE           Set environment variables KEY=VALUE   [$GIT_CI_ENV]
--env-file VALUE          Environment file path                 [$GIT_CI_ENV_FILE]
--pull                    Pull docker images (default: true)    [$GIT_CI_PULL]
--no-cache                Disable cache                         [$GIT_CI_NO_CACHE]
--volume, -V VALUE        Bind mount volumes (repeatable)
--network VALUE           Docker network mode (default: "bridge") [$GIT_CI_NETWORK]
--memory VALUE            Container memory limit (e.g., 2g, 512m) [$GIT_CI_MEMORY]
--cpus VALUE              Container CPU limit (e.g., 2, 0.5)   [$GIT_CI_CPUS]
```

```bash
git ci run
git ci r --job test --docker
git ci exec -f .gitlab-ci.yml --stage deploy
git ci run --docker --volume $PWD/data:/data:ro --memory 2g --cpus 2
git ci run --only "test-*" --except "test-integration"
git ci run --continue-on-error --parallel
```

### validate (aliases: check, v)

Validate pipeline syntax.

```
--file, -f VALUE      Pipeline file path                 [$GIT_CI_FILE]
--provider, -p VALUE  CI provider: github, gitlab, auto (default: "auto")
--strict              Enable strict validation
```

```bash
git ci validate
git ci check -f .gitlab-ci.yml
git ci v --strict --provider github
```

### init

Initialize a new pipeline.

```
--provider, -p VALUE  CI provider: github, gitlab (default: "github")
--template, -t VALUE  Template: basic, node, python, go, docker (default: "basic")
--output, -o VALUE    Output file path
--force               Overwrite existing file
```

```bash
git ci init
git ci init --provider gitlab --template python
git ci init -t go -o .github/workflows/ci.yml --force
```

### clean

Clean up resources.

```
--all, -a       Clean all resources
--containers    Clean containers only
--images        Clean images only
--cache         Clean cache only
--force, -f     Force cleanup
```

```bash
git ci clean --all
git ci clean --containers --force
git ci clean --images
```

### env

Manage environment variables.

```bash
# list environment variables
git ci env list

# set environment variables
git ci env set KEY=value OTHER=value2
git ci env set KEY=value --save --file .env

# load from file
git ci env load
git ci env load -f .env.production
```

### config

Manage configuration.

```bash
# show current configuration
git ci config show

# initialize configuration file
git ci config init
git ci config init --output custom-config.yml --force
```

## FEATURES IN DEPTH

### Matrix Strategy Expansion

Jobs with matrix strategies are expanded into concrete job instances via Cartesian product. Supports `include` entries to extend combinations and `exclude` entries to filter them out. Matrix values are injected as environment variables (both raw keys and `MATRIX_` prefixed).

```yaml
# GitHub Actions example — 2×2 = 4 combinations, minus 1 exclude, plus 1 include
strategy:
  matrix:
    os: [ubuntu-latest, macos-latest]
    go: ["1.21", "1.22"]
    include:
      - os: ubuntu-latest
        go: "1.22"
        experimental: true
    exclude:
      - os: macos-latest
        go: "1.21"
```

After expansion, the 4 combinations + 1 include - 1 exclude = 4 concrete jobs. Each gets fresh env vars like `os=ubuntu-latest`, `go=1.22`, `MATRIX_OS=UBUNTU_LATEST`, `MATRIX_GO_VERSION=1.22`.

### Dependency Resolution

Jobs with `needs` are resolved using Kahn's algorithm for topological sorting. Circular dependencies are detected and reported. Jobs are grouped into dependency levels for parallel execution — all jobs in a level can run concurrently once their dependencies complete.

```yaml
# Dependency chain: lint → test → build → deploy
jobs:
  lint:
    steps: [echo linting]
  test:
    needs: [lint]
    steps: [echo testing]
  build:
    needs: [lint, test]
    steps: [echo building]
  deploy:
    needs: [build]
    steps: [echo deploying]
```

### Docker Service Containers

When a job defines `services`, git-ci creates a dedicated bridge network, starts service containers with DNS aliases matching their names, and attaches the job container to the same network. Services are accessible by name from the job container. Cleanup is automatic.

```yaml
services:
  postgres:
    image: postgres:15
    env:
      POSTGRES_PASSWORD: test
  redis:
    image: redis:7
```

### Resource Limits

Container memory and CPU limits can be set via `--memory` and `--cpus` flags:

```bash
git ci run --docker --memory 2g --cpus 1.5
```

### Provider Auto-Detection

git-ci auto-detects the CI provider from:
1. File path patterns (`.github/workflows/*` → GitHub, `.gitlab-ci.yml` → GitLab)
2. File content analysis (`on:` + `runs-on:` → GitHub, `stages:` + `script:` → GitLab)
3. Environment variables (`GITHUB_ACTIONS`, `GITLAB_CI`)

### Strict Validation

Use `--strict` with the `validate` command for stricter checks:

```bash
git ci validate --strict
```

## CONFIGURATION

Create a `.git-ci.yml` file in your project root:

```yaml
defaults:
  runner: docker
  timeout: 30
  parallel: false
  max_parallel: 4

environment:
  CI: "true"
  GIT_CI: "true"

docker:
  pull: true
  network: bridge
  volumes:
    - ./cache:/cache

cache:
  enabled: true
  paths:
    - node_modules
    - .cache
    - vendor

artifacts:
  paths:
    - dist
    - build
  expire_in: 1 week
```

Initialize with: `git ci config init`

## ENVIRONMENT VARIABLES

All flags can be configured via environment variables:

| Variable | Description | Default |
|---|---|---|
| `GIT_CI_VERBOSE` | Enable verbose output | `false` |
| `GIT_CI_DEBUG` | Enable debug mode | `false` |
| `GIT_CI_QUIET` | Suppress output | `false` |
| `GIT_CI_CONFIG` | Config file path | |
| `GIT_CI_WORKDIR` | Working directory | `.` |
| `GIT_CI_FILE` | Pipeline file path | auto-detected |
| `GIT_CI_JOB` | Job name to run | |
| `GIT_CI_STAGE` | Stage name to run | |
| `GIT_CI_ONLY` | Run only these jobs | |
| `GIT_CI_EXCEPT` | Exclude these jobs | |
| `GIT_CI_DOCKER` | Use Docker runner | `false` |
| `GIT_CI_PODMAN` | Use Podman runner | `false` |
| `GIT_CI_DRY_RUN` | Dry run mode | `false` |
| `GIT_CI_PARALLEL` | Parallel execution | `false` |
| `GIT_CI_MAX_PARALLEL` | Max parallel jobs | NumCPU |
| `GIT_CI_CONTINUE_ON_ERROR` | Continue on error | `false` |
| `GIT_CI_TIMEOUT` | Job timeout (minutes) | `30` |
| `GIT_CI_ENV` | Environment variables | |
| `GIT_CI_ENV_FILE` | Env file path | |
| `GIT_CI_PULL` | Pull docker images | `true` |
| `GIT_CI_NO_CACHE` | Disable cache | `false` |
| `GIT_CI_NETWORK` | Docker network mode | `bridge` |
| `GIT_CI_MEMORY` | Container memory limit | |
| `GIT_CI_CPUS` | Container CPU limit | |

Additionally, git-ci sets these variables at runtime:
- `GIT_CI=true`
- `CI=true`
- `GIT_CI_VERSION=<version>`

## EXAMPLES

### GitHub Actions — Full Go CI Pipeline

```yaml
# .github/workflows/ci.yml
name: Go CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]

env:
  GO_VERSION: "1.22"

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Lint
        run: |
          go fmt ./...
          go vet ./...

  test:
    runs-on: ubuntu-latest
    needs: lint
    strategy:
      matrix:
        go-version: ["1.21", "1.22"]
        os: [ubuntu-latest, macos-latest]
    steps:
      - uses: actions/checkout@v4
      - name: Test
        run: go test -race ./...

  build:
    runs-on: ubuntu-latest
    needs: [lint, test]
    steps:
      - uses: actions/checkout@v4
      - name: Build
        run: go build -o app .
```

```bash
# Run the full pipeline
$ git ci run

# Run only the test matrix (4 jobs)
$ git ci run --job test

# Run a specific matrix combination
$ git ci run --job "test (ubuntu-latest, 1.22)"

# Run with Docker (uses catthehacker images)
$ git ci run --docker

# Dry-run to preview what would execute
$ git ci run --dry-run --verbose

# List all jobs in tree format
$ git ci ls

# Export as JSON for external tooling
$ git ci ls --format json | jq '.jobs'
```

### GitLab CI — Multi-Stage Pipeline

```yaml
# .gitlab-ci.yml
stages:
  - lint
  - test
  - build
  - deploy

variables:
  APP_VERSION: "1.0.0"
  CI_IMAGE: "golang:1.22"

default:
  image: ${CI_IMAGE}
  before_script:
    - go mod download

lint:
  stage: lint
  script:
    - golangci-lint run

test:
  stage: test
  script:
    - go test -race ./...
  artifacts:
    paths:
      - coverage.out

build:
  stage: build
  script:
    - go build -o app .
  artifacts:
    paths:
      - app
  needs:
    - lint
    - test

deploy:
  stage: deploy
  script:
    - echo "Deploying version ${APP_VERSION}..."
    - ./deploy.sh
  when: manual
  only:
    - main
```

```bash
# Run the full pipeline
$ git ci run -f .gitlab-ci.yml

# Run a specific stage only
$ git ci run --stage test

# Run with services (Docker)
$ git ci run --docker --job integration-test

# Validate with provider detection
$ git ci validate --provider gitlab
```

### Matrix Builds — Real-World Patterns

```yaml
# Multi-dimensional matrix with include/exclude
name: Matrix CI
on: [push, pull_request]
jobs:
  test:
    runs-on: ${{ matrix.os }}
    strategy:
      fail-fast: false
      max-parallel: 4
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
        node: ["16", "18", "20"]
        include:
          # Pin a specific version for coverage
          - os: ubuntu-latest
            node: "20"
            coverage: true
        exclude:
          # Don't test older Node on Windows
          - os: windows-latest
            node: "16"
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: ${{ matrix.node }}
      - name: Test
        run: npm test
      - name: Coverage
        if: matrix.coverage
        run: npm run coverage
```

```bash
# 3×3 = 9 combinations + 1 include - 1 exclude = 9 matrix jobs
$ git ci ls

# Run a specific matrix job by its expanded name
$ git ci run --job "test (ubuntu-latest, 20)"

# The expanded environment includes:
#   os=ubuntu-latest, node=20, coverage=true
#   MATRIX_OS=UBUNTU_LATEST, MATRIX_NODE=20, MATRIX_COVERAGE=true
```

### Service Containers — Integration Testing with Databases

```yaml
# .github/workflows/integration.yml
name: Integration Tests
on: push

jobs:
  integration:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:15
        env:
          POSTGRES_PASSWORD: testpass
          POSTGRES_DB: testdb
        ports:
          - 5432:5432
      redis:
        image: redis:7
        ports:
          - 6379:6379
    steps:
      - uses: actions/checkout@v4
      - name: Run tests
        run: |
          # Services are accessible by hostname (postgres, redis)
          pg_isready -h postgres -U postgres
          redis-cli -h redis ping
      - name: Run integration suite
        run: go test -tags=integration ./...
        env:
          DATABASE_URL: "postgres://postgres:testpass@postgres:5432/testdb?sslmode=disable"
          REDIS_URL: "redis://redis:6379"
```

```bash
# Run with Docker (required for service containers)
$ git ci run --docker --job integration

# Enable verbose mode to see service container logs
$ git ci run --docker --verbose --job integration

# Clean up resources after running
$ git ci clean --all --force
```

### Docker Runner — Resource Limits, Volumes, and Networks

```bash
# Run with specific resource constraints
$ git ci run --docker --memory 512m --cpus 0.5

# Mount additional volumes
$ git ci run --docker --volume $PWD/data:/data:ro \
  --volume /tmp/cache:/cache

# Use a custom network mode
$ git ci run --docker --network host

# Skip image pulling (use locally cached images)
$ git ci run --docker --pull=false

# Combine Docker with parallel execution
$ git ci run --docker --parallel --max-parallel 8 --continue-on-error
```

### Podman Runner — Rootless Containers

```bash
# Run with Podman directly
$ git ci run --podman

# Podman supports resource limits too
$ git ci run --podman --memory 1g --cpus 2

# Podman automatically handles SELinux labels for volumes
# If Podman is not installed, falls back to Docker with a warning

# Combined with job filtering
$ git ci run --podman --only "test-*" --except "test-e2e"
```

### Combined Features — Matrix + Services + Parallel

```yaml
name: Full Stack CI
on: push

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Lint
        run: make lint

  test:
    runs-on: ubuntu-latest
    needs: lint
    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_PASSWORD: test
      redis:
        image: redis:7-alpine
    strategy:
      matrix:
        go: ["1.21", "1.22", "1.23"]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: ${{ matrix.go }}
      - name: Test with coverage
        run: go test -race -coverprofile=coverage.out ./...
        env:
          DATABASE_URL: "postgres://postgres:test@postgres:5432/postgres?sslmode=disable"
          REDIS_URL: "redis://redis:6379"

  build:
    runs-on: ubuntu-latest
    needs: test
    steps:
      - uses: actions/checkout@v4
      - name: Build
        run: go build -o app .
```

```bash
# Run everything (wildcard matching)
$ git ci run --docker --parallel

# This expands lint → 3 test jobs (one per Go version, each with services) → build
# The test jobs run in parallel because they're all at the same dependency level

# Preview the dependency graph first
$ git ci ls
```

### Cross-Provider Comparison

The same pipeline expressed in both GitHub Actions and GitLab CI:

```yaml
# GitHub Actions — .github/workflows/ci.yml
name: Build & Test
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - run: go test ./...
  build:
    runs-on: ubuntu-latest
    needs: test
    steps:
      - run: go build .
```

```yaml
# GitLab CI — .gitlab-ci.yml
stages: [test, build]
test:
  stage: test
  script: go test ./...
build:
  stage: build
  script: go build .
  needs: [test]
```

```bash
# Both produce identical execution behavior:
$ git ci run    # auto-detects the provider from the file
$ git ci run -f .github/workflows/ci.yml
$ git ci run -f .gitlab-ci.yml
```

### Configuration — Multiple Scenarios

```yaml
# .git-ci.yml — Full configuration
defaults:
  runner: docker
  timeout: 30
  parallel: true
  max_parallel: 4
  continue_on_error: false
  verbose: true

environment:
  CI: "true"
  GIT_CI: "true"
  MY_CUSTOM_VAR: "hello"

docker:
  pull: true
  network: bridge
  registry: docker.io
  volumes:
    - ./cache:/cache
    - ./data:/data:ro

cache:
  enabled: true
  paths:
    - node_modules
    - .cache/go-build
    - vendor/bundle

artifacts:
  paths:
    - dist/
    - build/*.tar.gz
    - coverage/*.html
  expire_in: 2 weeks

hooks:
  before_job:
    - echo "Starting job..."
  after_job:
    - echo "Job finished"
  on_success:
    - echo "All jobs passed!"
  on_failure:
    - echo "A job failed"
```

```bash
# Use config file explicitly
$ git ci run --config .git-ci.yml

# Or auto-detect (looks for .git-ci.yml in current dir and home)
$ git ci run

# Show current config
$ git ci config show

# Initialize a fresh config
$ git ci config init
$ git ci config init --output .my-custom-config.yml --force
```

### Validation — Various Modes

```bash
# Basic validation with auto-detected provider
$ git ci validate
✓ Pipeline 'Go CI' is valid

# Explicit provider
$ git ci validate --provider gitlab
✓ Pipeline 'GitLab CI Pipeline' is valid

# Strict validation (checks runner, steps, env vars)
$ git ci validate --strict
✓ Pipeline 'Go CI' is valid

# Validate a specific file
$ git ci validate -f .github/deploy.yml

# Check alias
$ git ci check -f .gitlab-ci.yml --strict

# Verbose validation
$ git ci validate --verbose
```

### Environment Management

```bash
# List all GIT_CI related environment variables
$ git ci env list

# List all environment variables (including system)
$ git ci env list --verbose

# Set environment variables
$ git ci env set DATABASE_URL=postgres://localhost:5432/mydb \
  REDIS_URL=redis://localhost:6379

# Set and persist to .env file
$ git ci env set API_KEY=sk-abc123 --save --file .env

# Load from .env file
$ git ci env load
$ git ci env load -f .env.production

# Use env vars in pipeline runs
$ git ci run --env DATABASE_URL=postgres://localhost:5432/mydb \
  --env-file .env
```

### Job Filtering — Patterns and Wildcards

```bash
# Run a single job by exact name
$ git ci run --job test

# Wildcard — run all jobs starting with "test"
$ git ci run --only "test-*"

# Wildcard — run all jobs ending with "e2e"
$ git ci run --only "*-e2e"

# Exclude specific jobs
$ git ci run --except "test-e2e"

# Combine only and except
$ git ci run --only "test-*" --except "test-integration"

# Run by stage (GitLab)
$ git ci run --stage deploy

# Filter with parallel execution
$ git ci run --parallel --only "build-*" --max-parallel 2
```

### Debug and Verbose Output

```bash
# Normal output (recommended for most use cases)
$ git ci run

# Verbose — shows commands and parsed config
$ git ci run --verbose

# Debug — very detailed output including environment
$ git ci run --debug

# Dry run — shows what would execute without running
$ git ci run --dry-run --verbose

# Quiet — suppress all non-error output
$ git ci run --quiet

# Global flags come before subcommand
$ git ci --verbose --workdir /path/to/project run
```

### Pipeline Init Templates

```bash
# List available templates
$ git ci init --help

# GitHub Actions templates
$ git ci init --provider github --template basic    # Simple echo
$ git ci init --provider github --template go        # Go build & test
$ git ci init --provider github --template node      # Node.js with matrix
$ git ci init --provider github --template python    # Python + pytest
$ git ci init --provider github --template docker    # Docker build & push

# GitLab CI templates
$ git ci init --provider gitlab --template basic
$ git ci init --provider gitlab --template go
$ git ci init --provider gitlab --template node
$ git ci init --provider gitlab --template python
$ git ci init --provider gitlab --template docker

# Custom output path
$ git ci init -t go -o .github/workflows/ci.yml --force
$ git ci init -p gitlab -t python -o .gitlab-ci.yml
```

## DEVELOPMENT

```bash
# clone repository
git clone https://github.com/sanix-darker/git-ci
cd git-ci

# build
make build

# run tests
make test

# run all checks (fmt, vet, lint, test)
make check

# run CI pipeline locally
make ci

# generate coverage report
make coverage

# run benchmarks
make bench

# install locally
make install

# run in development mode (hot reload)
make dev

# list jobs using the built binary
make list

# run a specific job
make run-job JOB=test

# run a job with Docker
make run-docker JOB=test

# display project info
make info
```

## DOCKER

The project includes a multi-stage Dockerfile:

| Stage | Purpose |
|---|---|
| `development` | Full dev environment with Go toolchain, linter, and dependencies |
| `builder` | Compiles the binary with version injection via LDFLAGS |
| `test` | Runs unit tests with race detection and coverage |
| `runtime` | Minimal Ubuntu image with just the `gci` binary |
| `integration-test` | Runtime image with test fixtures |

```bash
# build Docker image
make docker

# run in Docker
make docker-run ARGS="ls"

# run full Docker test suite (unit + build + integration)
make docker-test

# run unit tests in Docker
make docker-unit

# run integration tests in Docker
make docker-integration
```

## CONTRIBUTING

Contributions are welcome!
Please read the contributing guidelines before submitting PRs.

## LICENSE

MIT License - see LICENSE file for details.

## AUTHOR

[sanix-darker](https://github.com/sanix-darker)
