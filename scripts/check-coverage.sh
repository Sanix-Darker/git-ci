#!/usr/bin/env bash

set -Eeuo pipefail

profile="${1:-coverage.out}"
minimum="${GCI_MIN_COVERAGE:-55.0}"

if [[ ! -f "${profile}" ]]; then
  echo "coverage profile not found: ${profile}" >&2
  exit 1
fi

actual="$(go tool cover -func="${profile}" | awk '/^total:/ {gsub(/%/, "", $3); print $3}')"
if [[ -z "${actual}" ]]; then
  echo "unable to read total coverage from ${profile}" >&2
  exit 1
fi

if ! awk -v actual="${actual}" -v minimum="${minimum}" 'BEGIN { exit(actual + 0 >= minimum + 0 ? 0 : 1) }'; then
  echo "coverage ${actual}% is below required ${minimum}%" >&2
  exit 1
fi

echo "coverage gate passed: ${actual}% >= ${minimum}%"
