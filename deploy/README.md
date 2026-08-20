# git-ci web deployment

This folder contains sample deployment configuration for the `git-ci` web API + dashboard.
All files are deployment *examples* only. Replace placeholder values locally before deploying.

## Files

- `docker-compose.yml` – containerized deployment using a `gci serve` backend + Caddy.
- `Dockerfile` – builds a minimal Linux image with `gci` and the dashboard assets.
- `Caddyfile` – production compose variant (reverse-proxies to `git-ci-site:8087`).
- `Caddyfile.example` – local/systemd variant example (reverse-proxies to `127.0.0.1:8087`).
- `git-ci-site.service` – bare-metal/systemd sample service for `gci serve`.
- `.env.example` – optional local override for the compose image name.
- API health probe: use `GET /api/health` or `GET /api/v1/health` (also `GET /health` is available).

## Option A: Docker Compose (recommended on a fresh host)

```bash
cd deploy
cp .env.example .env   # optional, if you want to pin image name
docker compose up -d --build
```

By default this exposes HTTPS on ports `80/443` via Caddy and serves both the API and
dashboard from `git-ci-site:8087`.

Before going online:

1. Edit `deploy/Caddyfile` and replace `gci.example.com` with your real domain.
2. Ensure DNS points to this host and port 80/443 are reachable.
3. Confirm:
   ```bash
   curl -I http://127.0.0.1
   curl -kI https://127.0.0.1
curl -fsS http://127.0.0.1:8087/health
   ```
4. Check in browser using the real domain.

5. Keep public route and health probe in sync:
   - DNS/edge should point this domain to the target host running this deployment.
   - If your origin is proxied (Cloudflare), ensure origin TLS mode and certificate strategy match your Caddy setup.
   - Quick probe (versioned contract preferred):
     ```bash
curl -s https://gci.example.com/api/v1/health
curl -s https://gci.example.com/api/v1/webhooks
     ```

You should see JSON like:

```json
{"status":"ok"}
```

## Option B: Bare-metal with systemd + existing Caddy

Use when you already have a shared host Caddy and only want this service on localhost.

1. Copy and edit the service file:
   ```bash
   sudo cp deploy/git-ci-site.service /etc/systemd/system/git-ci-site.service
   sudo systemctl daemon-reload
   sudo systemctl enable --now git-ci-site
   ```
2. Build/install the site from this repo under `/opt/git-ci` (or adjust `WorkingDirectory`/`GIT_CI_SITE_ROOT`).
3. Add a local reverse-proxy block for this site:
   - map `gci.example.com` to `127.0.0.1:8087`
   - if you already have a shared host Caddyfile, copy `Caddyfile.host-snippet`
     and paste it there.
4. Validate:
   ```bash
   systemctl is-active git-ci-site
   curl -fsS http://127.0.0.1:8087/api/v1/health
```

### Emergency recovery for gci.sanixdk.xyz (if service is stale)

If the site is still running the old static-file setup, replace `git-ci-site.service` with
an API-backed runtime using `gci serve`.

```ini
[Service]
ExecStart=/usr/local/bin/gci serve --listen 127.0.0.1:8087 --static-dir /opt/git-ci/site --api-prefix /api
```

Then verify from the host:

```bash
systemctl daemon-reload
systemctl restart git-ci-site
curl -fsS http://127.0.0.1:8087/api/v1/health
curl -fsS http://127.0.0.1:8087/api/jobs?workdir=/opt/git-ci
curl -fsS -X POST http://127.0.0.1:8087/api/v1/runs \
  -H 'Content-Type: application/json' \
  -d '{"workdir":"/opt/git-ci","file":".github/workflows/ci.yml"}'
```

If this works on localhost, validate public route:

```bash
curl -ks https://gci.sanixdk.xyz/api/v1/health
curl -ks https://gci.sanixdk.xyz/ | head -n 5
```

### Quick 525 troubleshooting

Cloudflare `525` usually means origin TLS/HTTPS handshake failure.

From the server (or another host inside your network), run:

```bash
openssl s_client -connect <origin-ip>:443 -servername gci.example.com < /dev/null
```

If the output includes `no peer certificate available`, the host Caddy config is
missing a matching TLS cert for that hostname or is not matching it on that host
block.

For this project on `gci.sanixdk.xyz`, the quickest fix is to add the concrete
block from `deploy/Caddyfile.sanixdk-host` to your shared host `/etc/caddy/Caddyfile`
and reload Caddy:

```bash
scp deploy/Caddyfile.sanixdk-host root@178.105.18.9:/etc/caddy/git-ci-host-snippet
ssh root@178.105.18.9 "printf '\nimport /etc/caddy/git-ci-host-snippet\n' >> /etc/caddy/Caddyfile && caddy reload --config /etc/caddy/Caddyfile"
```

If the next check still returns `525`, edit `Caddyfile.sanixdk-host` and
`Caddyfile.host-snippet` to use `tls` (instead of `tls internal`) first, then
reload Caddy.

If `gci.sanixdk.xyz` still shows the portfolio page, check host block ordering:

1. Ensure `gci.sanixdk.xyz` appears in `/etc/caddy/Caddyfile` above any
   `sanixdk.xyz` or catch-all block.
2. Remove/override any stale `git-ci` redirect rules in your CDN/DNS layer.
3. Confirm the upstream points to the local service:
   ```bash
curl -ks https://gci.sanixdk.xyz/health
curl -ks https://gci.sanixdk.xyz/api/v1/health
   ```
4. Confirm no process is serving on 443/80 with the same hostname for another site:
   ```bash
   ss -ltnp | rg ':443|:80'
   ```

Then verify on origin and edge:

```bash
openssl s_client -connect 178.105.18.9:443 -servername gci.sanixdk.xyz < /dev/null
curl -ksI https://gci.sanixdk.xyz/api/v1/health
```

Also verify API reachability through Caddy:

```bash
curl -ks https://gci.sanixdk.xyz/api/v1/health
curl -ks https://gci.sanixdk.xyz/api/v1/webhooks
```

You should see a certificate presented and a JSON payload from `/api/v1/health` (or `/api/health`).

### Quick incident loop

If the host is stale after a deploy or CDN/proxy change:

```bash
systemctl is-active git-ci-site
journalctl -u git-ci-site -n 120
systemctl restart git-ci-site
curl -fsS http://127.0.0.1:8087/api/v1/health
curl -fsS http://127.0.0.1:8087/api/v1/webhooks
```

Add the block from `deploy/Caddyfile.host-snippet` (or `deploy/Caddyfile.sanixdk-host` for this host) and reload/restart Caddy.

## Safety / hygiene

- This repo is public. Do not store credentials, API keys, tokens, real host credentials, or TLS material here.
- Use `Caddyfile.example` only as a template; keep real values local when deploying.
