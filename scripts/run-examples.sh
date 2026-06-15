#!/usr/bin/env bash
# =============================================================================
# run-examples.sh — Validate and dry-run all example pipeline files
# =============================================================================
# This script builds the gci binary, then iterates over every example pipeline
# file in examples/ and runs:
#   1. gci validate   — ensures the pipeline syntax is correct
#   2. gci ls         — lists the jobs in the pipeline
#   3. gci run --dry-run --verbose — previews execution
#
# Usage:
#   bash scripts/run-examples.sh              # run all examples
#   bash scripts/run-examples.sh --verbose     # with extra output
#   bash scripts/run-examples.sh --build-only  # just build, don't test
#   bash scripts/run-examples.sh --skip-build  # use existing binary
#
# Exit code: 0 if all examples pass, 1 if any fail.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
EXAMPLES_DIR="$PROJECT_DIR/examples"

BINARY="$PROJECT_DIR/build/gci"
PASS_COUNT=0
FAIL_COUNT=0
SKIP_COUNT=0
VERBOSE=false

# ---- Colour helpers ---------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Colour

pass_msg()  { echo -e "  ${GREEN}✓${NC} $1"; }
fail_msg()  { echo -e "  ${RED}✗${NC} $1"; }
skip_msg()  { echo -e "  ${YELLOW}○${NC} $1"; }
info_msg()  { echo -e "  ${CYAN}ℹ${NC} $1"; }
header()    { echo -e "\n${BOLD}$1${NC}"; echo "  $(printf '─%.0s' $(seq 1 72))"; }

# ---- Parse flags ------------------------------------------------------------
BUILD=true
for arg in "$@"; do
  case "$arg" in
    --verbose|-v)  VERBOSE=true ;;
    --build-only)  BUILD=true; RUN_TESTS=false ;;
    --skip-build)  BUILD=false ;;
    *)             echo "Unknown flag: $arg"; exit 1 ;;
  esac
done

# Default: run tests if not --build-only
RUN_TESTS="${RUN_TESTS:-true}"

# ---- Build ------------------------------------------------------------------
if [ "$BUILD" = true ]; then
  header "Building gci binary"
  cd "$PROJECT_DIR"
  mkdir -p build

  if go build -ldflags="-s -w" -o "$BINARY" ./cmd 2>&1; then
    pass_msg "Binary built: $BINARY"
  else
    fail_msg "Build failed"
    exit 1
  fi
else
  if [ ! -x "$BINARY" ]; then
    echo -e "${RED}Binary not found at $BINARY. Run without --skip-build or build manually.${NC}"
    exit 1
  fi
  info_msg "Using existing binary: $BINARY"
fi

BINARY_VERSION="$("$BINARY" --version 2>&1)"
info_msg "gci version: $BINARY_VERSION"

if [ "$RUN_TESTS" = false ]; then
  echo -e "\n${GREEN}Build complete. Skipping tests (--build-only).${NC}"
  exit 0
fi

# ---- Test functions ---------------------------------------------------------

# run_test <label> <command>
# Executes a command, prints pass/fail, and updates counters.
run_test() {
  local label="$1"
  shift

  if [ "$VERBOSE" = true ]; then
    echo ""
    info_msg "Running: $*"
  fi

  if "$@" > /tmp/gci-example-test.log 2>&1; then
    pass_msg "$label"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    fail_msg "$label"
    echo -e "    ${RED}Command:${NC} $*"
    echo -e "    ${RED}Output:${NC}"
    sed 's/^/    /' /tmp/gci-example-test.log
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# run_example_tests <file> <provider_flag>
# Runs validate, ls, and dry-run on a single example file.
run_example_tests() {
  local file="$1"
  local provider_flag="${2:---provider auto}"
  local rel_path="${file#$PROJECT_DIR/}"

  header "Testing: $rel_path"

  run_test "validate $rel_path" "$BINARY" validate -f "$file" $provider_flag
  run_test "ls $rel_path"       "$BINARY" ls -f "$file"

  # Dry-run output is streamed to the terminal for demonstration.
  # Note: --verbose is a global flag and must come BEFORE the subcommand.
  # --provider is not needed here because the run command auto-detects
  # the provider from the file path and content.
  echo ""
  info_msg "dry-run --verbose $rel_path"
  if "$BINARY" --verbose run --dry-run -f "$file" 2>/tmp/gci-example-dryrun.log; then
    pass_msg "dry-run $rel_path"
    PASS_COUNT=$((PASS_COUNT + 1))
  else
    fail_msg "dry-run $rel_path"
    echo -e "    ${RED}Dry-run stderr:${NC}"
    sed 's/^/    /' /tmp/gci-example-dryrun.log
    FAIL_COUNT=$((FAIL_COUNT + 1))
  fi
}

# ---- Define example files ---------------------------------------------------

GITHUB_FILES=(
  "$EXAMPLES_DIR/github/simple.yml"
  "$EXAMPLES_DIR/github/go-ci.yml"
  "$EXAMPLES_DIR/github/matrix.yml"
  "$EXAMPLES_DIR/github/services.yml"
  "$EXAMPLES_DIR/github/full-stack.yml"
)

GITLAB_FILES=(
  "$EXAMPLES_DIR/gitlab/simple.yml"
  "$EXAMPLES_DIR/gitlab/multi-stage.yml"
)

CIRCLECI_FILES=(
  "$EXAMPLES_DIR/circleci/config.yml"
)

DRONE_FILES=(
  "$EXAMPLES_DIR/drone/.drone.yml"
)

TRAVIS_FILES=(
  "$EXAMPLES_DIR/travis/.travis.yml"
)

# ---- Run tests --------------------------------------------------------------
echo ""
echo -e "${BOLD}gci Example Pipeline Test Suite${NC}"
echo "  $(printf '═%.0s' $(seq 1 72))"
echo "  Project:     $PROJECT_DIR"
echo "  Binary:      $BINARY ($BINARY_VERSION)"
echo "  Examples:    $EXAMPLES_DIR"
echo "  $(printf '═%.0s' $(seq 1 72))"

# GitHub Actions examples
header "GitHub Actions Examples (${#GITHUB_FILES[@]} files)"
for file in "${GITHUB_FILES[@]}"; do
  if [ -f "$file" ]; then
    run_example_tests "$file" "--provider github"
  else
    skip_msg "File not found: $file"
    SKIP_COUNT=$((SKIP_COUNT + 1))
  fi
done

# GitLab CI examples
header "GitLab CI Examples (${#GITLAB_FILES[@]} files)"
for file in "${GITLAB_FILES[@]}"; do
  if [ -f "$file" ]; then
    run_example_tests "$file" "--provider gitlab"
  else
    skip_msg "File not found: $file"
    SKIP_COUNT=$((SKIP_COUNT + 1))
  fi
done

# CircleCI examples
header "CircleCI Examples (${#CIRCLECI_FILES[@]} files)"
for file in "${CIRCLECI_FILES[@]}"; do
  if [ -f "$file" ]; then
    # CircleCI files use GitHub parser (with --provider github)
    run_example_tests "$file" "--provider github"
  else
    skip_msg "File not found: $file"
    SKIP_COUNT=$((SKIP_COUNT + 1))
  fi
done

# Drone CI examples
header "Drone CI Examples (${#DRONE_FILES[@]} files)"
for file in "${DRONE_FILES[@]}"; do
  if [ -f "$file" ]; then
    # Drone files fall back to GitHub parser
    run_example_tests "$file" "--provider github"
  else
    skip_msg "File not found: $file"
    SKIP_COUNT=$((SKIP_COUNT + 1))
  fi
done

# Travis CI examples
header "Travis CI Examples (${#TRAVIS_FILES[@]} files)"
for file in "${TRAVIS_FILES[@]}"; do
  if [ -f "$file" ]; then
    # Travis files fall back to GitHub parser
    run_example_tests "$file" "--provider github"
  else
    skip_msg "File not found: $file"
    SKIP_COUNT=$((SKIP_COUNT + 1))
  fi
done

# ---- Summary ----------------------------------------------------------------
TOTAL=$((PASS_COUNT + FAIL_COUNT + SKIP_COUNT))
echo ""
echo "  $(printf '═%.0s' $(seq 1 72))"
echo -e "  ${BOLD}Results:${NC}  ${GREEN}${PASS_COUNT} passed${NC}  ${RED}${FAIL_COUNT} failed${NC}  ${YELLOW}${SKIP_COUNT} skipped${NC}  (${TOTAL} total)"
echo ""

if [ "$FAIL_COUNT" -gt 0 ]; then
  exit 1
fi
