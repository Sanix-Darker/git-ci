#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
run_id="${GCI_E2E_RUN_ID:-$$}"
work_root="$(mktemp -d -t gci-public-e2e.XXXXXX)"
project_root="${work_root}/projects"
project_path="${project_root}/fixture"
state_volume="gci-public-state-${run_id}"
service_container="gci-service-e2e-${run_id}"
edge_container="gci-edge-e2e-${run_id}"
service_image="gci-service-e2e:${run_id}"
base_url="${GCI_E2E_BASE_URL:-http://127.0.0.1:18088}"
service_listen="127.0.0.1:18087"

cleanup() {
  docker rm -f "${edge_container}" "${service_container}" >/dev/null 2>&1 || true
  docker volume rm "${state_volume}" >/dev/null 2>&1 || true
  docker image rm "${service_image}" >/dev/null 2>&1 || true
  rm -rf "${work_root}"
}

request_status() {
  local method="$1"
  local path="$2"
  shift 2
  curl --silent --output /dev/null --write-out '%{http_code}' \
    --request "${method}" "${base_url}${path}" "$@"
}

assert_status() {
  local method="$1"
  local path="$2"
  local expected="$3"
  shift 3
  local actual
  actual="$(request_status "${method}" "${path}" "$@")"
  if [[ "${actual}" != "${expected}" ]]; then
    echo "${method} ${path}: expected ${expected}, got ${actual}" >&2
    return 1
  fi
}

trap cleanup EXIT

mkdir -p "${project_path}/.git"
chmod -R a+rX "${project_root}"
docker volume create "${state_volume}" >/dev/null
docker build --quiet --file "${repo_root}/deploy/Dockerfile" \
  --tag "${service_image}" "${repo_root}" >/dev/null
docker run --detach --name "${service_container}" \
  --network host \
  --env GIT_CI_LISTEN="${service_listen}" \
  --volume "${state_volume}:/var/lib/gci" \
  --volume "${project_root}:/projects:ro" \
  "${service_image}" >/dev/null
docker run --detach --name "${edge_container}" \
  --network host \
  --env GCI_ADDRESS=http://127.0.0.1:18088 \
  --env GCI_UPSTREAM="${service_listen}" \
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
assert_status GET /api/v1 401
assert_status GET /api/v1/projects 401
assert_status GET /app 303
assert_status GET /app/projects 303

health_body="$(curl --silent "${base_url}/healthz")"
if [[ "${health_body}" != *'"status":"ok"'* ]]; then
  echo "GET /healthz: expected service health JSON, got ${health_body@Q}" >&2
  exit 1
fi

frame_header="$(curl --silent --dump-header - --output /dev/null "${base_url}/" \
  | tr -d '\r' | awk -F': ' 'tolower($1) == "x-frame-options" {print $2}')"
if [[ "${frame_header}" != "DENY" ]]; then
  echo "GET /: expected X-Frame-Options DENY, got ${frame_header@Q}" >&2
  exit 1
fi

token="$(docker exec "${service_container}" cat /var/lib/gci/admin.token | tr -d '\r\n')"
api_root="$(curl --silent --header "Authorization: Bearer ${token}" "${base_url}/api/v1")"
if [[ "${api_root}" != *'"capabilities"'* ]]; then
  echo "GET /api/v1: missing versioned capability document" >&2
  exit 1
fi

create_payload='{"slug":"fixture","path":"/projects/fixture","defaultBranch":"master"}'
assert_status POST /api/v1/projects 201 \
  --header "Authorization: Bearer ${token}" \
  --header 'Content-Type: application/json' \
  --data "${create_payload}"

first_list="$(curl --silent --header "Authorization: Bearer ${token}" "${base_url}/api/v1/projects")"
if [[ "${first_list}" != *'"slug":"fixture"'* ]] || [[ "${first_list}" != *'"count":1'* ]]; then
  echo "created project is missing through the edge proxy" >&2
  exit 1
fi

docker restart "${service_container}" >/dev/null
for _ in $(seq 1 80); do
  if [[ "$(request_status GET /healthz 2>/dev/null || true)" == "200" ]]; then
    break
  fi
  sleep 0.25
done

restarted_token="$(docker exec "${service_container}" cat /var/lib/gci/admin.token | tr -d '\r\n')"
if [[ "${restarted_token}" != "${token}" ]]; then
  echo "admin token changed after service restart" >&2
  exit 1
fi
second_list="$(curl --silent --header "Authorization: Bearer ${token}" "${base_url}/api/v1/projects")"
if [[ "${second_list}" != *'"slug":"fixture"'* ]] || [[ "${second_list}" != *'"count":1'* ]]; then
  echo "SQLite project state did not survive service restart" >&2
  exit 1
fi

echo "public service E2E passed at ${base_url}"
