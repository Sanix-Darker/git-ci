#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
runtime="$repo_root/build/e2e-web"
project_root="$runtime/projects"
state_dir="$runtime/state"
binary="$runtime/gci"
port="${GCI_WEB_E2E_PORT:-18089}"

rm -rf "$runtime"
mkdir -p "$project_root/alpha-service/.git" "$project_root/beta-worker/.git" "$state_dir"
printf '%s\n' '# alpha service' >"$project_root/alpha-service/README.md"
printf '%s\n' '# beta worker' >"$project_root/beta-worker/README.md"

cd "$repo_root"
go build -o "$binary" ./cmd
exec "$binary" --workdir "$project_root" serve \
  --listen "127.0.0.1:$port" \
  --state-dir "$state_dir" \
  --static-dir "$repo_root/site" \
  --projects-root "$project_root"
