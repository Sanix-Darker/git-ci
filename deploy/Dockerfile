FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /out/gci cmd/cli.go

FROM alpine:3.22

RUN addgroup -S gci && adduser -S -G gci -s /sbin/nologin gci
RUN apk add --no-cache ca-certificates

WORKDIR /srv/git-ci
COPY --from=build /out/gci /usr/local/bin/gci
COPY site ./site

USER gci

EXPOSE 8087

ENTRYPOINT ["/usr/local/bin/gci"]
CMD ["serve", "--listen", ":8087", "--static-dir", "/srv/git-ci/site", "--api-prefix", "/api"]
