# git-ci


![asset](./asset.png)

The git alias you forgot you needed for CI locally,
gitlab, github, bitbucket, azure, jenkins...

```bash
$ git ci help
NAME:
   git-ci - Run CI/CD pipelines locally

USAGE:
   git-ci [global options] command [command options]

VERSION:
   f6d8421

AUTHOR:
   Sanix Darker <s4nixd@gmail.com>

COMMANDS:
   list, ls            List jobs and pipelines
   run, r, exec        Run jobs or pipelines
   validate, check, v  Validate pipeline syntax
   init                Initialize a new pipeline
   clean               Clean up resources
   env                 Manage environment variables
   config              Manage configuration
   help, h             Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --debug                    Enable debug mode (default: false) [$GIT_CI_DEBUG]
   --quiet, -q                Suppress output (default: false) [$GIT_CI_QUIET]
   --config value, -c value   Config file path [$GIT_CI_CONFIG]
   --workdir value, -w value  Working directory (default: ".") [$GIT_CI_WORKDIR]
   --help, -h                 show help
   --version, -v              print the version

COPYRIGHT:
   Copyright (c) 2025 Sanix Darker
```

---

```bash
$ git ci run -f ./.github/workflows/test-ci.yml
Running 1 job(s) sequentially
--------------------------------------------------------------------------------

================================================================================
 Running Job: Test Run
--------------------------------------------------------------------------------
 Working Directory: /home/dk/github/git-ci
 Runner: bash (native)
================================================================================

[1/3] Print environment
--------------------------------------------------------------------------------
  Node environment: test
  Working directory: /home/dk/github/git-ci
Step completed in 2ms

[2/3] Create test file
--------------------------------------------------------------------------------
Step completed in 2ms

[3/3] Verify file
--------------------------------------------------------------------------------
  test content from github
Step completed in 2ms

================================================================================
 Job 'Test Run' completed successfully
 Total duration: 12ms
================================================================================

Job 'test-run' succeeded in 12ms
--------------------------------------------------------------------------------
Pipeline completed in 12ms
Success: 1, Failed: 0, Total: 1
```
## REQUIREMENTS

- Go 1.23+ (recommended, for building from source)
- Docker (optional, for containerized runs)

## HOW INSTALL/GET

### Option 1: Install with Go

```bash
go install github.com/sanix-darker/git-ci@latest
```

After installation, the binary will be available as `git-ci` in your `$GOPATH/bin` directory.

### Option 2: Download Pre-built Binary

```bash
# Linux (amd64)
curl -L https://github.com/sanix-darker/git-ci/releases/latest/download/gci-linux-amd64 -o gci
chmod +x gci
sudo mv gci /usr/local/bin/

# macOS (Apple Silicon)
curl -L https://github.com/sanix-darker/git-ci/releases/latest/download/gci-darwin-arm64 -o gci
chmod +x gci
sudo mv gci /usr/local/bin/

# macOS (Intel)
curl -L https://github.com/sanix-darker/git-ci/releases/latest/download/gci-darwin-amd64 -o gci
chmod +x gci
sudo mv gci /usr/local/bin/

# Windows (PowerShell)
Invoke-WebRequest -Uri "https://github.com/sanix-darker/git-ci/releases/latest/download/gci-windows-amd64.exe" -OutFile "gci.exe"
```

### Option 3: Build from Source

```bash
git clone https://github.com/sanix-darker/git-ci.git
cd git-ci
make build
sudo cp build/gci /usr/local/bin/
```

## QUICK START

```bash
# List jobs
gci ls

# List jobs from a specific workflow file
gci ls -f ./.github/workflows/ci.yml

# Run all jobs at once
gci run

# Run specific job
gci run --job test

# To Run with Docker
gci run --docker

# Validate pipeline
gci validate
```

## BASIC USAGE

### GITHUB ACTIONS

```bash
# Run workflow
gci run -f .github/workflows/ci.yml

# Run specific job
gci run -j build -f .github/workflows/ci.yml

# Dry run
gci run --dry-run
```

### GITLAB CI
```bash
# Run pipeline
gci run -f .gitlab-ci.yml

# Run specific stage
gci run --stage test

# Run in parallel
gci run --parallel
```

## ENVIRONMENT VARIABLES

```bash
# Use environment variables
export GIT_CI_DOCKER=true
export GIT_CI_TIMEOUT=60
gci run

# Or use flags
gci run --docker --timeout 60

# Load from file
gci run --env-file .env
```

## CONFIGURATION

Example of an `.git-ci.yml` :

```yaml
# git-ci configuration file
# https://github.com/sanix-darker/git-ci

defaults:
    runner: bash
    timeout: 30
    max_parallel: 4
environment:
    CI: "true"
    GIT_CI: "true"
docker:
    pull: true
    network: bridge
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
        - coverage
    expire_in: 1 week
```

## AUTHOR

[sanix-darker](https://github.com/sanix-darker)
