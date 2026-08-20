# git-ci service deployment

These examples run the authenticated `git-ci serve` control plane. The landing
page, operator console, API, scheduler, worker, and SQLite persistence all come
from the same binary.

The service intentionally rejects public listen addresses. Keep it on
`127.0.0.1:8087` and terminate HTTPS with a trusted reverse proxy.

## Bare-metal systemd

This is the recommended VPS topology because it has one application process and
one state directory.

```bash
sudo useradd --system --home-dir /var/lib/gci --shell /usr/sbin/nologin gci
sudo install -d -o gci -g gci -m 0700 /var/lib/gci
sudo install -d -m 0755 /etc/gci
sudo install -m 0755 git-ci /usr/local/bin/git-ci
sudo install -m 0644 deploy/git-ci.service /etc/systemd/system/git-ci.service
sudo install -m 0600 deploy/git-ci.env.example /etc/gci/gci.env
sudo systemctl daemon-reload
sudo systemctl enable --now git-ci
```

Edit `/etc/gci/gci.env` first. Every `GIT_CI_PROJECTS_ROOT` path is an allowlist
boundary for projects selectable in the console. Give the `gci` user read access
to source repositories and the deployment permissions required by trusted jobs.

Install `Caddyfile.example` as a host block or merge
`Caddyfile.host-snippet` into an existing Caddy configuration. Do not publish
port `8087` through a firewall.

```bash
curl -fsS http://127.0.0.1:8087/healthz
sudo cat /var/lib/gci/admin.token
```

The bootstrap token is printed only on first startup and remains in the
mode-`0600` token file.

## Docker Compose

Compose builds the binary image, persists `/var/lib/gci`, and mounts a host
project root at `/projects` read-only. On a Linux VPS, both containers use host
networking: git-ci remains on loopback while Caddy owns ports `80/443`. The two
processes can restart independently without publishing the service port.

```bash
cd deploy
cp .env.example .env
# Edit GCI_ADDRESS and GCI_HOST_PROJECTS_ROOT.
docker compose up -d --build
docker compose exec git-ci cat /var/lib/gci/admin.token
```

The minimal image contains `bash`, `git`, and CA certificates. Prefer the
bare-metal binary or build a derived image when workflows require additional
toolchains. Mounting `/var/run/docker.sock` grants host-level control and is not
part of this safe default.

This Compose file targets Linux hosts. Docker Desktop's host-network behavior is
different; use the native binary deployment there instead.

## Production host

`Caddyfile.sanixdk-host` is the concrete shared-host block for
`gci.sanixdk.xyz`. It proxies the entire service, including authenticated
`/app` and `/api/v1`; the application enforces access control and CSRF.
`git-ci.sanixdk.xyz` remains a canonical redirect only.

## Verification

```bash
systemctl is-active git-ci
curl -fsS http://127.0.0.1:8087/healthz
curl -fsS https://gci.example.com/healthz
curl -o /dev/null -sS -w '%{http_code}\n' https://gci.example.com/api/v1
```

The last request must return `401` without credentials. Run
`make e2e-public` to exercise the binary plus Caddy topology in Docker.

Operational backup, restore, upgrade, and failure procedures are in
[`docs/SERVICE.md`](../docs/SERVICE.md). API examples are in
[`docs/API_V1.md`](../docs/API_V1.md).
