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
        run: printf 'tests passed\n'
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
cat >"$project_root/beta-worker/.gitlab-ci.yml" <<'YAML'
stages: [test]
worker-test:
  stage: test
  script:
    - printf 'worker passed\n'
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
