#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
runtime="$repo_root/build/e2e-web"
project_root="$runtime/projects"
state_dir="$runtime/state"
binary="$runtime/gci"
port="${GCI_WEB_E2E_PORT:-18089}"

rm -rf "$runtime"
mkdir -p "$project_root/alpha-service/.github/workflows" "$project_root/beta-worker" "$project_root/manual-service" "$state_dir"
printf '%s\n' '# alpha service' >"$project_root/alpha-service/README.md"
printf '%s\n' '# beta worker' >"$project_root/beta-worker/README.md"
printf '%s\n' '# manual service' >"$project_root/manual-service/README.md"
cat >"$project_root/alpha-service/.github/workflows/ci.yml" <<'YAML'
name: Alpha CI
on:
  push:
jobs:
  prepare:
    runs-on: ubuntu-latest
    steps:
      - name: Prepare
        run: printf 'prepared\n'
  test:
    needs: [prepare]
    runs-on: ubuntu-latest
    steps:
      - name: Test
        run: |
          mkdir -p dist .gci-cache
          printf 'tests passed\n'
          printf '# Test summary\n\n- suite: passed\n- artifact: alpha-build\n' >> "$GITHUB_STEP_SUMMARY"
          printf 'e2e artifact\n' > dist/app.txt
          printf 'cached\n' > .gci-cache/dependency
      - name: Cache dependencies
        uses: actions/cache@v4
        with:
          path: .gci-cache
          key: alpha-dependencies-v1
      - name: Upload build
        uses: actions/upload-artifact@v4
        with:
          name: alpha-build
          path: dist
      - name: Secret mask
        env:
          TOKEN: "${{ secrets.DEPLOY_TOKEN }}"
        run: |
          printf '%s\n' "$TOKEN"
          printf 'secret=%s\n' "$TOKEN" >> "$GITHUB_STEP_SUMMARY"
          runtime_mask="${TOKEN}-runtime"
          printf '::add-mask::%s\n' "$runtime_mask"
          printf 'dynamic=%s\n' "$runtime_mask"
          printf 'dynamic=%s\n' "$runtime_mask" >> "$GITHUB_STEP_SUMMARY"
          printf '::notice file=src/app.go,line=12,col=4,title=Compile hint::masked %s\n' "$runtime_mask"
          printf '::stop-commands::pause-token-123\n'
          printf '::warning::ignored warning\n'
          printf '::pause-token-123::\n'
          printf '::warning file=src/app.go,line=13::real warning\n'
          printf '::error file=src/app.go,line=14,title=Static check::diagnostic error\n'
          printf '::group::Runtime diagnostics\n'
          printf 'github group payload\n'
          printf '::endgroup::\n'
          printf '\033[0Ksection_start:1:gitlab_setup[collapsed=true]\r\033[0KGitLab setup\n'
          printf 'gitlab section payload\n'
          printf '\033[0Ksection_end:2:gitlab_setup\r\033[0K\n'
  deploy:
    needs: [test]
    runs-on: ubuntu-latest
    environment: production
    x-gci:
      rollback: printf 'rolled back %s\n' "$GCI_ROLLBACK_TARGET_SHA"
      verify: printf 'rollback verified\n'
    env:
      DEPLOY_TOKEN: "${{ secrets.DEPLOY_TOKEN }}"
    steps:
      - name: Deploy production
        run: printf 'deployed %s\n' "$DEPLOY_TOKEN"
YAML
cat >"$project_root/alpha-service/.github/workflows/matrix.yml" <<'YAML'
name: Matrix Preview
on: workflow_dispatch
concurrency:
  group: preview-${{ github.ref }}
  cancel-in-progress: true
jobs:
  verify:
    if: ${{ matrix.os != 'blocked' }}
    strategy:
      fail-fast: true
      max-parallel: 2
      matrix:
        os: [linux, windows]
    runs-on: ${{ matrix.os }}
    steps:
      - name: Verify target
        if: ${{ matrix.os == 'linux' }}
        run: printf '%s\n' '${{ matrix.os }}'
  publish:
    needs: verify
    runs-on: linux
    steps:
      - run: printf 'published\n'
YAML
cat >"$project_root/alpha-service/.github/workflows/failure.yml" <<'YAML'
name: Failure CI
on:
  workflow_dispatch:
    inputs:
      target:
        description: Deployment target
        required: true
        default: staging
        type: choice
        options: [staging, production]
      dry-run:
        description: Skip mutations
        required: true
        default: "true"
        type: boolean
jobs:
  fail:
    runs-on: ubuntu-latest
    steps:
      - name: Expected failure
        run: printf 'expected failure\n'; exit 9
YAML
cat >"$project_root/alpha-service/.github/workflows/runtime.yml" <<'YAML'
name: Runtime Topology
on: workflow_dispatch
jobs:
  integration:
    runs-on: ubuntu-latest
    container:
      image: alpine:3.20
      options: --cpus 1 --memory 256m --pids-limit 128
    services:
      redis:
        image: redis:7-alpine
        ports: [6379]
        options: --health-cmd "redis-cli ping" --health-interval 2s --health-timeout 1s --health-retries 20
    steps:
      - name: Probe service
        run: printf 'runtime topology\n'
YAML
cat >"$project_root/alpha-service/.github/workflows/shared.yml" <<'YAML'
name: Shared Verify
on:
  workflow_call:
    inputs:
      target:
        required: true
        type: string
jobs:
  compile:
    runs-on: ubuntu-latest
    steps:
      - name: Shared compile
        run: printf 'compile %s\n' '${{ inputs.target }}'
  audit:
    needs: compile
    runs-on: ubuntu-latest
    steps:
      - name: Shared audit
        run: printf 'audit complete\n'
YAML
cat >"$project_root/alpha-service/.github/workflows/reuse.yml" <<'YAML'
name: Reusable Delivery
on: workflow_dispatch
jobs:
  prepare:
    runs-on: ubuntu-latest
    steps:
      - run: printf 'reuse prepared\n'
  shared:
    needs: prepare
    uses: ./.github/workflows/shared.yml
    with:
      target: production
  publish:
    needs: shared
    runs-on: ubuntu-latest
    steps:
      - run: printf 'reuse published\n'
YAML
mkdir -p "$project_root/alpha-service/.github/actions/check"
cat >"$project_root/alpha-service/.github/actions/check/action.yml" <<'YAML'
name: Local Check
inputs:
  target:
    required: true
runs:
  using: composite
  steps:
    - name: Prepare input
      shell: bash
      run: printf '%s' '${{ inputs.target }}' > composite-target.txt
    - name: Verify input
      shell: bash
      env:
        EXPECTED: ${{ inputs.target }}
      run: test "$(cat composite-target.txt)" = "$EXPECTED"
YAML
cat >"$project_root/alpha-service/.github/workflows/composite.yml" <<'YAML'
name: Composite Delivery
on: workflow_dispatch
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - name: Local check
        uses: ./.github/actions/check
        with:
          target: service
      - name: Finish
        run: test -f composite-target.txt
YAML
cat >"$project_root/alpha-service/.github/workflows/gpu.yml" <<'YAML'
name: GPU Delivery
on: workflow_dispatch
jobs:
  accelerate:
    runs-on: [self-hosted, linux, x64, gpu]
    steps:
      - name: Probe GPU
        run: printf 'gpu ready\n'
YAML
cat >"$project_root/manual-service/.gitlab-ci.yml" <<'YAML'
stages: [build, deploy, verify]
manual-prepare:
  stage: build
  script:
    - printf 'manual prepared\n'
manual-release:
  stage: deploy
  needs: [manual-prepare]
  when: manual
  allow_failure: false
  manual_confirmation: Release this commit to production?
  script:
    - printf 'release %s\n' "$RELEASE_NOTE"
manual-verify:
  stage: verify
  needs: [manual-release]
  script:
    - printf 'manual verified\n'
YAML
cat >"$project_root/beta-worker/.gitlab-ci.yml" <<'YAML'
stages: [test]
default:
  image: alpine:3.20
  services:
    - name: redis:7-alpine
      alias: cache
worker-test:
  stage: test
  script:
    - printf 'worker passed\n'
  artifacts:
    name: worker-report
    paths: [report.xml]
    reports:
      junit: report.xml
YAML

for project in "$project_root/alpha-service" "$project_root/beta-worker" "$project_root/manual-service"; do
	git -C "$project" init --quiet --initial-branch=main
	git -C "$project" config user.email git-ci@example.invalid
	git -C "$project" config user.name "git-ci E2E"
	git -C "$project" add --all
	git -C "$project" commit --quiet --message "E2E fixture snapshot"
done

cd "$repo_root"
go build -o "$binary" ./cmd
exec "$binary" --workdir "$project_root" serve \
	--listen "127.0.0.1:$port" \
	--state-dir "$state_dir" \
	--projects-root "$project_root"
