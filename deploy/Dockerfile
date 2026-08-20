FROM golang:1.24.9-alpine AS build

RUN apk add --no-cache git
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=unknown
ARG BRANCH=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.Commit=${COMMIT} -X main.BuildTime=${BUILD_TIME} -X main.Branch=${BRANCH}" \
    -o /out/git-ci ./cmd

FROM alpine:3.22

RUN apk add --no-cache bash ca-certificates git \
    && addgroup -S -g 10001 gci \
    && adduser -S -D -u 10001 -G gci -h /var/lib/gci gci \
    && install -d -o gci -g gci -m 0700 /var/lib/gci \
    && install -d -o gci -g gci -m 0755 /projects

COPY --from=build /out/git-ci /usr/local/bin/git-ci

ENV GIT_CI_LISTEN=127.0.0.1:8087 \
    GIT_CI_STATE_DIR=/var/lib/gci \
    GIT_CI_PROJECTS_ROOT=/projects

USER gci
VOLUME ["/var/lib/gci"]
EXPOSE 8087
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
    CMD wget -qO- "http://${GIT_CI_LISTEN}/healthz" >/dev/null || exit 1

ENTRYPOINT ["git-ci"]
CMD ["serve"]
