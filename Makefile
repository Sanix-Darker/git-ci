BINARY_NAME := gci
PACKAGE := github.com/sanix-darker/git-ci
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_TIME := $(shell date -u +%Y%m%d.%H%M%S)
BRANCH := $(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")

# Go related variables
GO := go
GOFLAGS := -v
GOOS := $(shell go env GOOS)
GOARCH := $(shell go env GOARCH)
GOPATH := $(shell go env GOPATH)
GOBIN := $(GOPATH)/bin

# Build directories
BUILD_DIR := build
DIST_DIR := dist
COVERAGE_DIR := coverage

# Source files
MAIN_FILE := cmd/cli.go
SRC_FILES := $(shell find . -type f -name '*.go' -not -path "./vendor/*")

# Test files
TEST_FILES := $(shell find . -type f -name '*_test.go' -not -path "./vendor/*")

# Build flags
LDFLAGS := -ldflags="-w -s \
	-X main.Version=$(VERSION) \
	-X main.Commit=$(COMMIT) \
	-X main.BuildTime=$(BUILD_TIME) \
	-X main.Branch=$(BRANCH)"

# Default target
.DEFAULT_GOAL := help

# Phony targets
.PHONY: all build clean test fmt vet lint run install uninstall \
        deps vendor docker release tag help coverage bench \
        check build-all ci dev watch

## help: Display this help message
help:
	@echo "git-ci Makefile"
	@echo "==============="
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Available targets:"
	@awk '/^[a-zA-Z\-\_0-9]+:/ { \
		helpMessage = match(lastLine, /^## (.*)/); \
		if (helpMessage) { \
			helpCommand = substr($$1, 0, index($$1, ":")-1); \
			helpMessage = substr(lastLine, RSTART + 3, RLENGTH); \
			printf "  %-20s %s\n", helpCommand, helpMessage; \
		} \
	} \
	{ lastLine = $$0 }' $(MAKEFILE_LIST)

## all: Build the binary for current platform
all: build

## build: Build the binary for current platform
build: deps
	@echo "Building $(BINARY_NAME) $(VERSION) for $(GOOS)/$(GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	@$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) $(MAIN_FILE)
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

## build-all: Build for multiple platforms
build-all: deps
	@echo "Building for multiple platforms..."
	@mkdir -p $(DIST_DIR)

	# Linux builds
	@GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-amd64 $(MAIN_FILE)
	@GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-arm64 $(MAIN_FILE)
	# @GOOS=linux GOARCH=386 $(GO) build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-linux-386 $(MAIN_FILE)

	# macOS builds
	@GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-amd64 $(MAIN_FILE)
	@GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-darwin-arm64 $(MAIN_FILE)

	# Windows builds
	# @GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_FILE)
	# @GOOS=windows GOARCH=386 $(GO) build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY_NAME)-windows-386.exe $(MAIN_FILE)

	@echo "Multi-platform build complete. Binaries in $(DIST_DIR)/"
	@ls -lah $(DIST_DIR)/

## run: Build and run the binary
run: build
	@echo "Running $(BINARY_NAME)..."
	@./$(BUILD_DIR)/$(BINARY_NAME) $(ARGS)

## install: Install the binary to GOPATH/bin
install: build
	@echo "Installing $(BINARY_NAME) to $(GOBIN)..."
	@cp $(BUILD_DIR)/$(BINARY_NAME) $(GOBIN)/
	@chmod +x $(GOBIN)/$(BINARY_NAME)
	@echo "Installed to $(GOBIN)/$(BINARY_NAME)"
	@echo "Make sure $(GOBIN) is in your PATH"

## uninstall: Remove the binary from GOPATH/bin
uninstall:
	@echo "Uninstalling $(BINARY_NAME)..."
	@rm -f $(GOBIN)/$(BINARY_NAME)
	@echo "Uninstalled"

## clean: Remove build artifacts
clean:
	@echo "Cleaning up..."
	@rm -rf $(BUILD_DIR) $(DIST_DIR) $(COVERAGE_DIR)
	@rm -f $(BINARY_NAME)
	@rm -f coverage.out coverage.html
	@rm -f *.test
	@rm -f *.prof
	@echo "Clean complete"

## deps: Download module dependencies
deps:
	@echo "Downloading dependencies..."
	@$(GO) mod download
	@$(GO) mod verify
	@echo "Dependencies downloaded"

## vendor: Create vendor directory with all dependencies
vendor: deps
	@echo "Creating vendor directory..."
	@$(GO) mod vendor
	@echo "Vendor directory created"

## tidy: Clean up go.mod and go.sum
tidy:
	@echo "Tidying module dependencies..."
	@$(GO) mod tidy
	@echo "Module dependencies tidied"

## fmt: Format all Go source files
fmt:
	@echo "Formatting code..."
	@gofmt -s -w $(SRC_FILES)
	@goimports -w $(SRC_FILES) 2>/dev/null || true
	@echo "Code formatted"

## vet: Run go vet
vet:
	@echo "Running go vet..."
	@$(GO) vet ./...
	@echo "Vet complete"

## lint: Run linting
lint:
	@echo "Running linter..."
	@# Try golangci-lint first
	@if command -v golangci-lint > /dev/null 2>&1; then \
		(golangci-lint run --timeout 5m --disable-all \
			--enable=gofmt \
			--enable=govet \
			--enable=errcheck \
			--enable=ineffassign \
			--enable=staticcheck \
			./... 2>&1 | grep -v "goanalysis_metalinter" || true) && \
		echo "golangci-lint check complete"; \
	else \
		echo "golangci-lint not available, using basic Go tools..."; \
		echo "  Running gofmt..."; \
		test -z "$$(gofmt -l .)" || (echo "Files need formatting:"; gofmt -l .; exit 1); \
		echo "  Running go vet..."; \
		go vet ./...; \
		echo "  Running staticcheck (if available)..."; \
		(command -v staticcheck > /dev/null 2>&1 && staticcheck ./...) || echo "    staticcheck not installed"; \
	fi
	@echo "Lint complete"

## lint-basic: Run basic Go linting without golangci-lint
lint-basic:
	@echo "Running basic Go linting..."
	@echo "Checking formatting..."
	@test -z "$$(gofmt -l .)" || (echo "Files need formatting:"; gofmt -l .; exit 1)
	@echo "Running go vet..."
	@go vet ./...
	@echo "Basic lint complete"

## test: Run all tests
test:
	@echo "Running tests..."
	@$(GO) test -v -race ./...
	@echo "Tests complete"

## test-short: Run short tests only
test-short:
	@echo "Running short tests..."
	@$(GO) test -v -short ./...

## coverage: Generate test coverage report
coverage:
	@echo "Generating coverage report..."
	@mkdir -p $(COVERAGE_DIR)
	@$(GO) test -v -race -coverprofile=$(COVERAGE_DIR)/coverage.out ./...
	@$(GO) tool cover -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@echo "Coverage report generated: $(COVERAGE_DIR)/coverage.html"
	@echo "Coverage: $$(go tool cover -func=$(COVERAGE_DIR)/coverage.out | grep total | awk '{print $$3}')"

## bench: Run benchmarks
bench:
	@echo "Running benchmarks..."
	@$(GO) test -bench=. -benchmem ./...
	@echo "Benchmarks complete"

## check: Run all checks (fmt, vet, lint, test)
check: fmt vet lint test
	@echo "All checks passed!"

## ci: Run CI pipeline locally
ci: clean deps check build
	@echo "CI pipeline complete!"

## docker: Build Docker image
docker:
	@echo "Building Docker image..."
	@docker build -t $(BINARY_NAME):$(VERSION) -t $(BINARY_NAME):latest .
	@echo "Docker image built: $(BINARY_NAME):$(VERSION)"

## docker-run: Run the application in Docker
docker-run: docker
	@echo "Running in Docker..."
	@docker run --rm -v $(PWD):/workspace $(BINARY_NAME):latest $(ARGS)

## release: Create release artifacts
release: clean build-all
	@echo "Creating release artifacts..."
	@mkdir -p $(DIST_DIR)/releases

	# Create tar.gz archives for Unix systems
	@cd $(DIST_DIR) && tar czf releases/$(BINARY_NAME)-$(VERSION)-linux-amd64.tar.gz $(BINARY_NAME)-linux-amd64
	@cd $(DIST_DIR) && tar czf releases/$(BINARY_NAME)-$(VERSION)-linux-arm64.tar.gz $(BINARY_NAME)-linux-arm64
	# @cd $(DIST_DIR) && tar czf releases/$(BINARY_NAME)-$(VERSION)-linux-386.tar.gz $(BINARY_NAME)-linux-386
	@cd $(DIST_DIR) && tar czf releases/$(BINARY_NAME)-$(VERSION)-darwin-amd64.tar.gz $(BINARY_NAME)-darwin-amd64
	@cd $(DIST_DIR) && tar czf releases/$(BINARY_NAME)-$(VERSION)-darwin-arm64.tar.gz $(BINARY_NAME)-darwin-arm64

	# Create zip archives for Windows
	# @cd $(DIST_DIR) && zip -q releases/$(BINARY_NAME)-$(VERSION)-windows-amd64.zip $(BINARY_NAME)-windows-amd64.exe
	# @cd $(DIST_DIR) && zip -q releases/$(BINARY_NAME)-$(VERSION)-windows-386.zip $(BINARY_NAME)-windows-386.exe

	# Generate checksums
	@cd $(DIST_DIR)/releases && sha256sum *.tar.gz *.zip > checksums.txt

	@echo "Release artifacts created in $(DIST_DIR)/releases/"
	@ls -lah $(DIST_DIR)/releases/

## tag: Create a new git tag
tag:
	@if [ -z "$(TAG)" ]; then \
		echo "Usage: make tag TAG=v1.0.0"; \
		exit 1; \
	fi
	@echo "Creating tag $(TAG)..."
	@git tag -a $(TAG) -m "Release $(TAG)"
	@echo "Tag $(TAG) created"
	@echo "Run 'git push origin $(TAG)' to push the tag"

## push-tag: Push the latest tag to origin
push-tag:
	@echo "Pushing tags to origin..."
	@git push origin --tags
	@echo "Tags pushed"

## version: Display version information
version:
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Branch: $(BRANCH)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo "Go Version: $(shell go version)"

## dev: Run in development mode with hot reload
dev:
	@echo "Starting development mode..."
	@if command -v air > /dev/null; then \
		air; \
	else \
		echo "Air not installed. Installing..."; \
		go install github.com/cosmtrek/air@latest; \
		air; \
	fi

## watch: Watch for file changes and rebuild
watch:
	@echo "Watching for changes..."
	@if command -v watchexec > /dev/null; then \
		watchexec -r -e go -- make build; \
	else \
		echo "watchexec not installed. Install with: cargo install watchexec-cli"; \
		exit 1; \
	fi

## init: Initialize project (install tools)
init:
	@echo "Initializing project..."
	@go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@go install golang.org/x/tools/cmd/goimports@latest
	@go install github.com/cosmtrek/air@latest
	@echo "Project initialized"

## update: Update all dependencies
update:
	@echo "Updating dependencies..."
	@$(GO) get -u ./...
	@$(GO) mod tidy
	@echo "Dependencies updated"

## list: List all available workflow files
list:
	@echo "Listing workflow files..."
	@./$(BUILD_DIR)/$(BINARY_NAME) ls

## run-job: Run a specific job (use JOB=jobname)
run-job: build
	@if [ -z "$(JOB)" ]; then \
		echo "Usage: make run-job JOB=test"; \
		exit 1; \
	fi
	@echo "Running job: $(JOB)..."
	@./$(BUILD_DIR)/$(BINARY_NAME) run --job $(JOB) $(ARGS)

## run-docker: Run a job with Docker (use JOB=jobname)
run-docker: build
	@if [ -z "$(JOB)" ]; then \
		echo "Usage: make run-docker JOB=test"; \
		exit 1; \
	fi
	@echo "Running job with Docker: $(JOB)..."
	@./$(BUILD_DIR)/$(BINARY_NAME) run --job $(JOB) --docker $(ARGS)

## info: Display project information
info:
	@echo "Project: $(BINARY_NAME)"
	@echo "Package: $(PACKAGE)"
	@echo "Version: $(VERSION)"
	@echo "Commit: $(COMMIT)"
	@echo "Branch: $(BRANCH)"
	@echo "Build Time: $(BUILD_TIME)"
	@echo ""
	@echo "Go Environment:"
	@echo "  GOOS: $(GOOS)"
	@echo "  GOARCH: $(GOARCH)"
	@echo "  GOPATH: $(GOPATH)"
	@echo "  Go Version: $(shell go version)"
	@echo ""
	@echo "Source Statistics:"
	@echo "  Go files: $(words $(SRC_FILES))"
	@echo "  Test files: $(words $(TEST_FILES))"
	@echo "  Lines of code: $(shell find . -name '*.go' -not -path './vendor/*' | xargs wc -l | tail -1 | awk '{print $$1}')"
