#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
run_id="${GCI_E2E_RUN_ID:-$$}"
network="gci-public-e2e-${run_id}"
site_container="gci-site-e2e-${run_id}"
edge_container="gci-edge-e2e-${run_id}"
site_image="gci-site-e2e:${run_id}"
base_url="${GCI_E2E_BASE_URL:-http://127.0.0.1:18088}"

cleanup() {
  docker rm -f "${edge_container}" "${site_container}" >/dev/null 2>&1 || true
  docker network rm "${network}" >/dev/null 2>&1 || true
}

request_status() {
  local method="$1"
  local path="$2"
  curl --silent --output /dev/null --write-out '%{http_code}' \
    --request "${method}" "${base_url}${path}"
}

assert_status() {
  local method="$1"
  local path="$2"
  local expected="$3"
  local actual
  actual="$(request_status "${method}" "${path}")"
  if [[ "${actual}" != "${expected}" ]]; then
    echo "${method} ${path}: expected ${expected}, got ${actual}" >&2
    return 1
  fi
}

trap cleanup EXIT

docker network create "${network}" >/dev/null
docker build --quiet --file "${repo_root}/deploy/Dockerfile" \
  --tag "${site_image}" "${repo_root}" >/dev/null
docker run --detach --name "${site_container}" --network "${network}" \
  "${site_image}" >/dev/null
docker run --detach --name "${edge_container}" --network "${network}" \
  --publish 127.0.0.1:18088:8088 \
  --env GCI_SITE_ADDRESS=http://:8088 \
  --env GCI_UPSTREAM="${site_container}:8087" \
  --volume "${repo_root}/deploy/Caddyfile:/etc/caddy/Caddyfile:ro" \
  caddy:2-alpine >/dev/null

for _ in $(seq 1 40); do
  if [[ "$(request_status GET /healthz 2>/dev/null || true)" == "200" ]]; then
    break
  fi
  sleep 0.25
done

assert_status GET / 200
assert_status GET /healthz 200
assert_status GET /health 200
assert_status GET /api 404
assert_status GET /api/v1/health 404
assert_status POST /api/v1/runs 404
assert_status GET /app 404
assert_status GET /app/projects 404

health_body="$(curl --silent "${base_url}/healthz")"
if [[ "${health_body}" != "OK" ]]; then
  echo "GET /healthz: expected body OK, got ${health_body@Q}" >&2
  exit 1
fi

frame_header="$(curl --silent --dump-header - --output /dev/null "${base_url}/" \
  | tr -d '\r' | awk -F': ' 'tolower($1) == "x-frame-options" {print $2}')"
if [[ "${frame_header}" != "DENY" ]]; then
  echo "GET /: expected X-Frame-Options DENY, got ${frame_header@Q}" >&2
  exit 1
fi

echo "public surface E2E passed at ${base_url}"
