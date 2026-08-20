#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
runtime="$repo_root/build/e2e-web"
project_root="$runtime/projects"
state_dir="$runtime/state"
binary="$runtime/gci"
port="${GCI_WEB_E2E_PORT:-18089}"

rm -rf "$runtime"
mkdir -p "$project_root/alpha-service/.github/workflows" "$project_root/beta-worker" "$state_dir"
printf '%s\n' '# alpha service' >"$project_root/alpha-service/README.md"
printf '%s\n' '# beta worker' >"$project_root/beta-worker/README.md"
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
        run: printf '%s\n' "$TOKEN"
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
cat >"$project_root/beta-worker/.gitlab-ci.yml" <<'YAML'
stages: [test]
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

for project in "$project_root/alpha-service" "$project_root/beta-worker"; do
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
