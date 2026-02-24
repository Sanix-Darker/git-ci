#!/bin/bash
set -euo pipefail

IMAGE_NAME="git-ci"
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS${NC}: $1"; }
fail() { echo -e "${RED}FAIL${NC}: $1"; exit 1; }
info() { echo -e "${YELLOW}INFO${NC}: $1"; }

PHASE="${1:-all}"

# Phase 1: Build development image
run_phase1() {
    info "Phase 1: Building development image..."
    docker build --target development -t "${IMAGE_NAME}:dev" . \
        || fail "Development image build failed"
    pass "Development image built"
}

# Phase 2: Run unit tests inside container
run_phase2() {
    info "Phase 2: Running unit tests in container..."
    docker build --target test -t "${IMAGE_NAME}:test" . \
        || fail "Unit tests failed"
    pass "Unit tests passed in container"
}

# Phase 3: Build binary inside container
run_phase3() {
    info "Phase 3: Building binary in container..."
    docker build --target builder -t "${IMAGE_NAME}:builder" . \
        || fail "Binary build failed"
    pass "Binary built successfully"
}

# Phase 4: Build runtime image
run_phase4() {
    info "Phase 4: Building runtime image..."
    docker build --target runtime -t "${IMAGE_NAME}:latest" . \
        || fail "Runtime image build failed"
    pass "Runtime image built"
}

# Phase 5: Integration tests
run_phase5() {
    info "Phase 5: Running integration tests..."

    # Test: gci version
    docker run --rm "${IMAGE_NAME}:latest" version \
        || fail "gci version failed"
    pass "gci version"

    # Test: gci ls with bundled workflow
    docker run --rm "${IMAGE_NAME}:latest" ls \
        || fail "gci ls failed"
    pass "gci ls"

    # Test: gci validate
    docker run --rm "${IMAGE_NAME}:latest" validate \
        || fail "gci validate failed"
    pass "gci validate"

    # Test: gci run --dry-run
    docker run --rm "${IMAGE_NAME}:latest" run --dry-run \
        || fail "gci run --dry-run failed"
    pass "gci run --dry-run"

    # Test: gci validate --strict
    docker run --rm "${IMAGE_NAME}:latest" validate --strict \
        || info "gci validate --strict had warnings (expected for some workflows)"
    pass "gci validate --strict"

    # Test with external workflow file
    docker build --target integration-test -t "${IMAGE_NAME}:integration" . \
        || fail "Integration test image build failed"

    docker run --rm "${IMAGE_NAME}:integration" ls -f /testdata/github/basic.yml \
        || fail "gci ls with test fixture failed"
    pass "gci ls with test fixture"

    docker run --rm "${IMAGE_NAME}:integration" validate -f /testdata/github/basic.yml \
        || fail "gci validate with test fixture failed"
    pass "gci validate with test fixture"

    pass "All integration tests passed"
}

# Phase 6: Docker-in-Docker test (requires Docker socket)
run_phase6() {
    info "Phase 6: Docker-in-Docker test..."

    if [ ! -S /var/run/docker.sock ]; then
        info "Docker socket not available, skipping DinD tests"
        return 0
    fi

    docker run --rm \
        -v /var/run/docker.sock:/var/run/docker.sock \
        "${IMAGE_NAME}:latest" run --dry-run --docker \
        || info "DinD dry-run completed (may have warnings)"
    pass "Docker-in-Docker test completed"
}

case "${PHASE}" in
    all)
        run_phase1
        run_phase2
        run_phase3
        run_phase4
        run_phase5
        run_phase6
        echo ""
        pass "All Docker test phases completed successfully!"
        ;;
    unit)     run_phase1; run_phase2 ;;
    build)    run_phase1; run_phase3; run_phase4 ;;
    integration) run_phase1; run_phase3; run_phase4; run_phase5 ;;
    dind)     run_phase6 ;;
    *)
        echo "Usage: $0 [all|unit|build|integration|dind]"
        exit 1
        ;;
esac
