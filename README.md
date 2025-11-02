# git-ci


![asset](./asset.png)

The git alias you forgot you needed for CI locally, gitlab, github, bitbucket, azure, jenkins...


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

## INSTALL

```bash
# From source
go install github.com/sanix-darker/git-ci@latest

# Or download binary
curl -L https://github.com/sanix-darker/git-ci/releases/latest/download/gci-$(uname -s)-$(uname -m) -o gci
chmod +x gci
sudo mv gci /usr/local/bin/
```

## QUICK START

```bash
# List jobs
gci ls

# Run all jobs
gci run

# Run specific job
gci run --job test

# Run with Docker
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

[dk](https://github.com/sanix-darker)
