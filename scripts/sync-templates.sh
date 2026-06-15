#!/usr/bin/env bash
# =============================================================================
# sync-templates.sh — Generate example files from `gci init` templates
# =============================================================================
# This script runs `gci init` for every provider/template combination and
# saves the generated pipelines to examples/<provider>/templates/.
#
# It ensures the examples always reflect the current template definitions
# in internal/handlers/init.go. Run this after modifying templates.
#
# Usage:
#   bash scripts/sync-templates.sh                   # generate all templates
#   bash scripts/sync-templates.sh --check           # verify examples are up-to-date (exit 1 if not)
#   bash scripts/sync-templates.sh --verbose          # show each command
#   bash scripts/sync-templates.sh --skip-build       # use existing build/gci binary
#   bash scripts/sync-templates.sh --build-only       # build without generating
#
# Exit code: 0 if all templates sync or are in sync, 1 on error or drift.
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BINARY="$PROJECT_DIR/build/gci"
TEMPLATES_DIR="$PROJECT_DIR/examples"
TMPDIR=""
VERBOSE=false
CHECK_MODE=false
BUILD=true
RUN_SYNC=true
PASS_COUNT=0
FAIL_COUNT=0

# ---- Colour helpers ---------------------------------------------------------
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

pass_msg()  { echo -e "  ${GREEN}✓${NC} $1"; }
fail_msg()  { echo -e "  ${RED}✗${NC} $1"; }
info_msg()  { echo -e "  ${CYAN}ℹ${NC} $1"; }
header()    { echo -e "\n${BOLD}$1${NC}"; echo "  $(printf '─%.0s' $(seq 1 72))"; }

# ---- Cleanup on exit --------------------------------------------------------
cleanup() {
  if [ -n "$TMPDIR" ] && [ -d "$TMPDIR" ]; then
    rm -rf "$TMPDIR"
  fi
}
trap cleanup EXIT

# ---- Parse flags ------------------------------------------------------------
for arg in "$@"; do
  case "$arg" in
    --check)       CHECK_MODE=true ;;
    --verbose|-v)  VERBOSE=true ;;
    --skip-build)  BUILD=false ;;
    --build-only)  BUILD=true; RUN_SYNC=false ;;
    *)             echo "Unknown flag: $arg"; exit 1 ;;
  esac
done

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

if [ "$RUN_SYNC" = false ]; then
  echo -e "\n${GREEN}Build complete. Skipping template sync (--build-only).${NC}"
  exit 0
fi

# ---- Define provider/template matrix ----------------------------------------
# Each entry: provider|template|output_filename
declare -a ENTRIES=()

add_templates() {
  local provider="$1"
  shift
  for template in "$@"; do
    ENTRIES+=("$provider|$template")
  done
}

add_templates "github" "basic" "node" "python" "go" "docker"
add_templates "gitlab"  "basic" "node" "python" "go" "docker"
add_templates "circleci" "basic" "node" "python" "go" "docker"
add_templates "drone"    "basic" "node" "python" "go" "docker"
add_templates "travis"   "basic" "node" "python" "go" "docker"
add_templates "bitbucket" "basic"
add_templates "azure" "basic"

# ---- Generate / Check templates --------------------------------------------
if [ "$CHECK_MODE" = true ]; then
  header "Checking template sync status"
  TMPDIR="$(mktemp -d)"
  DRIFT=0
else
  if [ "$BUILD" = false ] || [ "${CHECK_MODE}" = false ]; then
    header "Generating template examples"
  fi
fi

for entry in "${ENTRIES[@]}"; do
  IFS='|' read -r provider template <<< "$entry"
  output_dir="$TEMPLATES_DIR/$provider/templates"
  output_file="$output_dir/$template.yml"
  mkdir -p "$output_dir"

  if [ "$VERBOSE" = true ]; then
    info_msg "Processing $provider/$template..."
  fi

  if [ "$CHECK_MODE" = true ]; then
    # Generate into temp directory and diff with existing
    tmp_file="$TMPDIR/$provider-$template.yml"

    if "$BINARY" init --provider "$provider" --template "$template" -o "$tmp_file" --force > /dev/null 2>&1; then
      if [ -f "$output_file" ]; then
        if diff -q "$tmp_file" "$output_file" > /dev/null 2>&1; then
          pass_msg "$provider/$template.yml — in sync"
        else
          fail_msg "$provider/$template.yml — DRIFTED (run without --check to regenerate)"
          if [ "$VERBOSE" = true ]; then
            echo ""
            diff -u "$output_file" "$tmp_file" 2>/dev/null | head -40
            echo ""
          fi
          DRIFT=$((DRIFT + 1))
        fi
      else
        fail_msg "$provider/$template.yml — MISSING (run without --check to create)"
        DRIFT=$((DRIFT + 1))
      fi
    else
      fail_msg "$provider/$template.yml — template generation failed"
      DRIFT=$((DRIFT + 1))
    fi
  else
    # Generate into the examples directory
    if "$BINARY" init --provider "$provider" --template "$template" -o "$output_file" --force > /dev/null 2>&1; then
      pass_msg "Generated $provider/templates/$template.yml"
      PASS_COUNT=$((PASS_COUNT + 1))
    else
      fail_msg "Failed to generate $provider/templates/$template.yml"
      FAIL_COUNT=$((FAIL_COUNT + 1))
    fi
  fi
done

# ---- Results ----------------------------------------------------------------
echo ""
if [ "$CHECK_MODE" = true ]; then
  echo "  $(printf '═%.0s' $(seq 1 72))"
  if [ "$DRIFT" -eq 0 ]; then
    echo -e "  ${GREEN}All templates are in sync!${NC}"
    exit 0
  else
    echo -e "  ${RED}$DRIFT template(s) have drifted or are missing.${NC}"
    echo "  Run without --check to regenerate:  bash scripts/sync-templates.sh"
    exit 1
  fi
else
  TOTAL=$((PASS_COUNT + FAIL_COUNT))
  echo "  $(printf '═%.0s' $(seq 1 72))"
  echo -e "  ${BOLD}Generated:${NC}  ${GREEN}${PASS_COUNT} success${NC}  ${RED}${FAIL_COUNT} failed${NC}  (${TOTAL} total)"
  echo ""
  echo "  Templates written to: $TEMPLATES_DIR/<provider>/templates/"
  echo ""
  echo "  Next step: run the test suite to verify all examples work:"
  echo "    bash scripts/run-examples.sh"
  echo ""

  if [ "$FAIL_COUNT" -gt 0 ]; then
    exit 1
  fi
fi
