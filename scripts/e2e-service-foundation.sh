#!/usr/bin/env bash

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work_root="$(mktemp -d -t gci-service-e2e.XXXXXX)"
project_root="${work_root}/projects"
project_path="${project_root}/fixture"
state_dir="${work_root}/state"
static_dir="${work_root}/site"
binary="${work_root}/gci"
service_version="v9.8.7-e2e"
port="${GCI_SERVICE_E2E_PORT:-18087}"
base_url="http://127.0.0.1:${port}"
service_pid=""

stop_service() {
  if [[ -n "${service_pid}" ]] && kill -0 "${service_pid}" 2>/dev/null; then
    kill "${service_pid}"
    wait "${service_pid}" || true
  fi
  service_pid=""
}

cleanup() {
  stop_service
  rm -rf "${work_root}"
}

status() {
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
  actual="$(status "${method}" "${path}" "$@")"
  if [[ "${actual}" != "${expected}" ]]; then
    echo "${method} ${path}: expected ${expected}, got ${actual}" >&2
    return 1
  fi
}

start_service() {
  "${binary}" --workdir "${project_root}" serve \
    --listen "127.0.0.1:${port}" \
    --state-dir "${state_dir}" \
    --static-dir "${static_dir}" \
    --projects-root "${project_root}" \
    >"${work_root}/service.log" 2>&1 &
  service_pid="$!"

  for _ in $(seq 1 80); do
    if [[ "$(status GET /healthz 2>/dev/null || true)" == "200" ]]; then
      return 0
    fi
    if ! kill -0 "${service_pid}" 2>/dev/null; then
      echo "service exited during startup" >&2
      sed -n '1,120p' "${work_root}/service.log" >&2
      return 1
    fi
    sleep 0.25
  done
  echo "service did not become healthy" >&2
  return 1
}

trap cleanup EXIT

mkdir -p "${project_path}/.git" "${static_dir}"
printf '%s\n' '<!doctype html><title>gci e2e</title><p>service-home</p>' >"${static_dir}/index.html"
printf '%s\n' 'OK' >"${static_dir}/healthz"

cd "${repo_root}"
go build -ldflags="-X main.Version=${service_version}" -o "${binary}" ./cmd
export GIT_CI_VERSION="v0.0.1-stale"

if "${binary}" --workdir "${project_root}" serve \
  --listen ":18088" --state-dir "${state_dir}-public" \
  --static-dir "${static_dir}" --projects-root "${project_root}" \
  >"${work_root}/public-bind.log" 2>&1; then
  echo "service accepted a public listen address" >&2
  exit 1
fi

start_service

for secret_file in admin.token session.key; do
  mode="$(stat -c '%a' "${state_dir}/${secret_file}")"
  if [[ "${mode}" != "600" ]]; then
    echo "${secret_file}: expected mode 600, got ${mode}" >&2
    exit 1
  fi
done
token="$(tr -d '\r\n' <"${state_dir}/admin.token")"

assert_status GET / 200
assert_status GET /healthz 200
assert_status GET /api/v1/projects 401
assert_status GET /login 200
assert_status GET /app 303

login_page="$(curl --silent "${base_url}/login")"
if [[ "${login_page}" != *'OPERATOR GATE'* ]] || [[ "${login_page}" != *'htmx.min.js'* ]]; then
	echo "login page is missing the HTMX operator surface" >&2
	exit 1
fi
health_payload="$(curl --silent "${base_url}/healthz")"
if [[ "${health_payload}" != *"\"version\":\"${service_version}\""* ]]; then
	echo "health endpoint did not prefer compiled version ${service_version}: ${health_payload}" >&2
	exit 1
fi
if [[ "${login_page}" != *"/ui/assets/app.css?v=${service_version}"* ]]; then
	echo "login page did not use compiled version as its asset cache key" >&2
	exit 1
fi

create_payload="$(printf '{"slug":"fixture","path":"%s","defaultBranch":"master"}' "${project_path}")"
assert_status POST /api/v1/projects 201 \
  --header "Authorization: Bearer ${token}" \
  --header 'Content-Type: application/json' \
  --data "${create_payload}"

first_list="$(curl --silent --header "Authorization: Bearer ${token}" "${base_url}/api/v1/projects")"
if [[ "${first_list}" != *'"slug":"fixture"'* ]] || [[ "${first_list}" != *'"count":1'* ]]; then
  echo "created project missing from first response" >&2
  exit 1
fi

stop_service
start_service

second_list="$(curl --silent --header "Authorization: Bearer ${token}" "${base_url}/api/v1/projects")"
if [[ "${second_list}" != *'"slug":"fixture"'* ]] || [[ "${second_list}" != *'"count":1'* ]]; then
  echo "project did not survive service restart" >&2
  exit 1
fi

echo "service foundation E2E passed at ${base_url}"
