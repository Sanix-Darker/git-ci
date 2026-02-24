# Stage 1: Development - full dev environment with tools
FROM golang:1.24-bookworm AS development

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    bash \
    curl \
    make \
    && rm -rf /var/lib/apt/lists/*

RUN go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Stage 2: Builder - compiles the binary
FROM development AS builder

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG BRANCH=unknown

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-w -s \
        -X main.Version=${VERSION} \
        -X main.Commit=${COMMIT} \
        -X main.BuildTime=${BUILD_TIME} \
        -X main.Branch=${BRANCH}" \
    -o /build/gci cmd/cli.go

# Stage 3: Test - runs unit tests
FROM development AS test

RUN go test -v -race -coverprofile=/coverage.out ./... \
    && go tool cover -func=/coverage.out

# Stage 4: Runtime - minimal image with just the binary
FROM ubuntu:24.04 AS runtime

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    bash \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /build/gci /usr/local/bin/gci

WORKDIR /workspace
ENTRYPOINT ["gci"]

# Stage 5: Integration test - runtime image with test fixtures
FROM runtime AS integration-test

COPY internal/parsers/testdata /testdata
COPY .github /workspace/.github
