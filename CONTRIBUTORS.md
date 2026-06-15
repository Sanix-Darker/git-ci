# Contributing

Thank you for your interest in contributing to **git-ci**! This document provides guidelines and workflows for contributors.

## Table of Contents

- [Development Setup](#development-setup)
- [Project Architecture](#project-architecture)
- [Coding Standards](#coding-standards)
- [Testing Guidelines](#testing-guidelines)
- [Git Workflow](#git-workflow)
- [Commit Messages](#commit-messages)
- [Pull Request Process](#pull-request-process)
- [Code Review Process](#code-review-process)
- [CI/CD Requirements](#cicd-requirements)
- [Pull Request Checklist](#pull-request-checklist)
- [Getting Help](#getting-help)

---

## Development Setup

### Prerequisites

- **Go 1.24+** (see `go.mod` for the exact toolchain version)
- **Docker** or **Podman** (optional, needed for container runner testing)

### Clone and Build

```bash
git clone https://github.com/sanix-darker/git-ci
cd git-ci

# Build the binary
make build

# Run tests
make test

# Install locally
make install        # copies to $GOPATH/bin
# or
make go-install     # uses go install ./cmd
```

### IDE Setup

The project uses standard Go tooling. For VS Code / GoLand / vim-go:

- Run `go mod tidy` after changing dependencies
- Run `gofmt -s` (or `make fmt`) before committing
- Enable `go vet` as a save hook or CI check

### Makefile Targets

| Command | Purpose |
|---------|---------|
| `make build` | Build the binary for the current platform |
| `make test` | Run all tests with race detection |
| `make fmt` | Format code with `gofmt -s` |
| `make vet` | Run `go vet ./...` |
| `make lint` | Run linters (golangci-lint or fallback) |
| `make check` | Run fmt → vet → lint → test in sequence |
| `make ci` | Full CI pipeline locally (clean → deps → check → build) |
| `make coverage` | Generate HTML coverage report |
| `make bench` | Run benchmarks |
| `make tidy` | Tidy `go.mod` and `go.sum` |

---

## Project Architecture

### Directory Layout

```
.
├── cmd/
│   └── cli.go              # Main entry point (package main)
├── internal/
│   ├── config/             # Configuration loading & defaults
│   ├── handlers/           # CLI command handlers (list, run, validate, etc.)
│   ├── parsers/            # CI provider parsers (GitHub, GitLab)
│   │   ├── github.go       # GitHub Actions parser
│   │   ├── gitlab.go       # GitLab CI parser
│   │   └── testdata/       # YAML fixtures for parser tests
│   └── runners/            # Execution backends (Bash, Docker, Podman)
│       ├── bash.go         # Native shell execution
│       ├── docker.go       # Docker container runner
│       ├── podman.go       # Podman runner
│       └── common.go       # Shared output formatting & utilities
├── pkg/
│   └── types/
│       └── types.go        # Universal types: Pipeline, Job, Step, Runner, etc.
├── .github/workflows/      # CI/CD workflows
├── docs/                   # Documentation
├── Makefile                # Build and development automation
└── go.mod / go.sum          # Go module files
```

### Key Design Principles

- **Provider-agnostic core**: All CI providers (GitHub, GitLab, etc.) parse into the same `Pipeline`/`Job`/`Step` types in `pkg/types`.
- **Parser interface**: Each provider implements `types.Parser` (Parse, ParseDirectory, Validate, GetProviderName).
- **Runner interface**: Each execution backend implements `types.Runner` (RunJob, RunStep, Cleanup, GetRunnerType).
- **Output formatting**: All runners share `OutputFormatter` in `internal/runners/common.go` for consistent CLI output.

---

## Coding Standards

### Go Style

- Follow [Go's official style guide](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments).
- Format all code with `gofmt -s` before committing (`make fmt`).
- Run `go vet ./...` to catch common issues (`make vet`).

### Naming Conventions

- **Variables**: camelCase (e.g., `workflowFile`, `pipelineName`)
- **Exported functions/types**: PascalCase (e.g., `CmdRun`, `GithubParser`)
- **Unexported helpers**: camelCase (e.g., `parseInput`, `detectParser`)
- **Test functions**: `Test<Package>_<Feature>` or `Test<FunctionName>_<Scenario>` (e.g., `TestTopologicalSort_CircularDep`, `TestValidatePipeline_Strict`)
- **YAML/JSON fields**: snake_case (e.g., `runs-on`, `timeout-minutes`)

### Error Handling

- Return errors — don't panic or call `os.Exit` outside `cmd/cli.go`.
- Wrap errors with context using `fmt.Errorf("context: %w", err)`.
- Use `printVerbose` and `printDebug` helpers from `internal/handlers/common.go` for conditional logging.

### Imports

- Group standard library imports first, then third-party, then internal.
- Use `goimports` to auto-sort (the `make fmt` target runs it if available).

```go
import (
    "fmt"
    "os"
    "strings"

    "github.com/sanix-darker/git-ci/internal/config"
    "github.com/sanix-darker/git-ci/pkg/types"
    cli "github.com/urfave/cli/v2"
)
```

### When Adding New Features

1. **Types first**: If your feature introduces new data structures, add them to `pkg/types/types.go`.
2. **Parser updates**: Update the appropriate parser(s) to handle the new fields from YAML configs.
3. **Handler wiring**: Wire the new behavior through command handlers in `internal/handlers/`.
4. **Runner support**: Implement execution logic in the relevant runner(s) (`bash.go`, `docker.go`, `podman.go`).
5. **CLI flags**: Add any new CLI flags in `cmd/cli.go` with proper env var bindings.
6. **Documentation**: Update `README.md` and add examples to the EXAMPLES section.
7. **Validation docs**: Add validation checks to `docs/VALIDATION.md`.

---

## Testing Guidelines

### Running Tests

```bash
# Run all tests with race detection
make test
# or
go test -v -race ./...

# Run tests for a specific package
go test -v -race ./internal/handlers/
go test -v -race ./internal/parsers/
go test -v -race ./internal/runners/

# Run short tests only (skips integration-heavy tests)
go test -v -short ./...

# Generate coverage report
make coverage
```

### Test Patterns

- **Package tests**: Use `package handlers` (not `package handlers_test`) for white-box testing of unexported functions.
- **Test data**: Place YAML/JSON fixtures in `internal/parsers/testdata/<provider>/` for parser tests.
- **Table-driven tests**: Prefer table-driven tests with descriptive sub-test names:

```go
func TestNormalizeMatrixEnvKey(t *testing.T) {
    cases := map[string]string{
        "go-version": "GO_VERSION",
        "os":         "OS",
        "node_js":    "NODE_JS",
    }
    for input, expected := range cases {
        t.Run(input, func(t *testing.T) {
            if got := normalizeMatrixEnvKey(input); got != expected {
                t.Errorf("normalizeMatrixEnvKey(%q) = %q, want %q", input, got, expected)
            }
        })
    }
}
```

- **Race detection**: Always run with `-race` flag. The CI workflow requires this.
- **Coverage**: Aim for >70% coverage on new code. Run `make coverage` to check.

### What to Test

- **Parsers**: Parse each test fixture, verify correct conversion to `types.Pipeline`, test error paths (invalid YAML, missing files).
- **Handlers**: Validate pipeline logic, topological sort, matrix expansion edge cases (empty, single, complex include/exclude).
- **Runners**: Output formatting, duration formatting, text wrapping, expression resolution.
- **Config**: Default config creation, cache/config directory resolution.

---

## Git Workflow

### Branch Naming

Use descriptive branch names with a category prefix:

- `feat/short-description` — New features (e.g., `feat/azure-parser`)
- `fix/short-description` — Bug fixes (e.g., `fix/matrix-include-empty`)
- `docs/short-description` — Documentation changes (e.g., `docs/contributing-guide`)
- `refactor/short-description` — Code refactoring (e.g., `refactor/handler-structure`)
- `test/short-description` — Test additions or improvements (e.g., `test/docker-runner-coverage`)
- `chore/short-description` — Tooling, CI, or dependency updates (e.g., `chore/update-actions`)

### Branch From Master

```bash
git checkout master
git pull upstream master
git checkout -b feat/my-feature
```

### Keep Your Branch Updated

```bash
git fetch upstream
git rebase upstream/master
# or
git merge upstream/master
```

Rebasing is preferred for a clean history, but merging is also acceptable.

---

## Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/) for clear and structured commit history:

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

### Types

| Type | When to Use |
|------|-------------|
| `feat` | A new feature (parser, runner, handler, flag) |
| `fix` | A bug fix |
| `docs` | Documentation changes (README, docs/, comments) |
| `style` | Code formatting, whitespace, missing semicolons |
| `refactor` | Code change that neither fixes a bug nor adds a feature |
| `test` | Adding or updating tests |
| `chore` | Tooling, CI configuration, dependency updates |
| `perf` | Performance improvements |

### Scope Examples

`parsers`, `handlers`, `runners`, `config`, `cli`, `docs`, `docker`, `matrix`, `toposort`, `validation`

### Examples

```
feat(parsers): add Azure DevOps pipeline parser

Parse azure-pipelines.yml into the universal Pipeline type.
Supports stages, jobs, steps, variables, and triggers.

Closes #42
```

```
fix(runners): handle empty matrix exclude gracefully

An empty exclude list was causing a nil pointer dereference
during matrix expansion. Now returns early when exclude is nil.

Fixes #87
```

```
docs(readme): add Podman runner examples and installation notes
```

```
chore(deps): update urfave/cli to v2.27.7
```

---

## Pull Request Process

### Before Submitting

1. **Discuss first**: Open an issue to discuss significant changes before implementing.
2. **Rebase**: Ensure your branch is up-to-date with `master`.
3. **Run checks**: Execute `make check` locally — this runs fmt, vet, lint, and tests.
4. **Coverage**: Confirm existing tests still pass; add tests for new functionality.
5. **Self-review**: Review your diff one more time for any issues.

### PR Title and Description

- **Title**: Follow the commit message convention (e.g., `feat(runners): add Kubernetes runner`).
- **Description**: Include:
  - What the change does and why
  - How to test it (commands to run)
  - Related issue number (e.g., `Closes #42`)
  - Any breaking changes or migration notes

### Small, Focused PRs

- Keep each PR focused on a single concern.
- A PR should typically touch 1-3 files unless it's a cross-cutting change (e.g., adding a new runner).
- Large PRs are harder to review. Break them into logical commits or separate PRs.

---

## Code Review Process

### Review Expectations

- Every PR needs **at least one approval** from a maintainer before merging.
- Address all review comments. Use 👍 / "Done" to acknowledge, or push fixes.
- If a comment is stylistic and not critical, reviewers should label it as "nitpick" — these can be resolved at your discretion.

### What Reviewers Look For

- **Correctness**: Does the code do what it claims? Are edge cases handled?
- **Testing**: Are there tests for new functionality? Do existing tests still pass?
- **Code style**: Is the code idiomatic Go? Is it consistent with the rest of the project?
- **Error handling**: Are errors handled gracefully? Are user-facing error messages clear?
- **Documentation**: Are new CLI flags, features, or config options documented?
- **Backward compatibility**: Does the change break existing workflows?

### Review Process

1. **Author submits PR** with clear description.
2. **Reviewer(s) comment** — may request changes or ask questions.
3. **Author responds** — pushes fixes or discusses alternatives.
4. **Approval** — reviewer approves when satisfied.
5. **Merge** — maintainer merges to `master` (squash merge preferred for clean history).

---

## CI/CD Requirements

### GitHub Actions Workflow

The project uses GitHub Actions (`.github/workflows/ci.yml`) for CI. The pipeline runs on push and PR to `master`:

1. **Lint** — Runs `make lint` (golangci-lint + gofmt + govet).
2. **Test** — Runs `go test -v -race -coverprofile=coverage.out ./...` with required system libraries.
3. **Build** — Builds binaries for linux/amd64 with version injection via LDFLAGS.
4. **Self-Test** — Runs the built binary through `--version`, `ls`, `run --dry-run`, and `validate`.

### Required Checks Before Merge

All CI checks must pass:

- [ ] `make lint` completes without warnings
- [ ] `make test` passes (all tests, race detector clean)
- [ ] `make build` produces a working binary
- [ ] Self-test passes (version, list, dry-run, validate)

### Local CI Simulation

Run the same checks locally before pushing:

```bash
make check   # fmt → vet → lint → test
make build   # ensure the binary compiles
```

---

## Pull Request Checklist

Before submitting your PR, verify:

- [ ] I have discussed the changes in an issue first (for significant changes)
- [ ] My code follows the project's coding standards (`make fmt`, `make vet` pass)
- [ ] I have added tests for my changes and they pass (`make test`)
- [ ] I have updated documentation (README, CLI help text, or docs/ files)
- [ ] I have updated the VALIDATION.md if adding new features or providers
- [ ] I have written meaningful commit messages following conventional commits
- [ ] My branch is up-to-date with `master` (rebased or merged)
- [ ] My PR is focused on a single concern
- [ ] I have checked for any breaking changes and documented them

---

## Getting Help

- **Issues**: Use [GitHub Issues](https://github.com/sanix-darker/git-ci/issues) for bug reports and feature requests.
- **Discussions**: Start a [GitHub Discussion](https://github.com/sanix-darker/git-ci/discussions) for design questions.
- **Existing PRs**: Check open/closed PRs to see if someone else has already worked on similar changes.

---

Thank you for your contribution! Every issue, PR, and discussion helps make git-ci better for everyone.
