# git-ci web deployment

This folder contains sample deployment configuration for the `git-ci` web landing page (`site/`).
All files are deployment *examples* only. Replace placeholder values locally before deploying.

## Files

- `docker-compose.yml` – containerized deployment using a dedicated static web service + Caddy.
- `Dockerfile` – builds a small static image for `site/` using `caddy file-server`.
- `Caddyfile` – production compose variant (reverse-proxies to `git-ci-site:8080`).
- `Caddyfile.example` – local/systemd variant example (reverse-proxies to `127.0.0.1:8080`).
- `git-ci-site.service` – bare-metal/systemd sample service for the static site.
- `.env.example` – optional local override for the compose image name.
- `/health` probe: Caddy responds with HTTP `202` so you can add an easy uptime check.

## Option A: Docker Compose (recommended on a fresh host)

```bash
cd deploy
cp .env.example .env   # optional, if you want to pin image name
docker compose up -d --build
```

By default this exposes HTTPS on ports `80/443` via Caddy and serves `site/` through
`git-ci-site:8080`.

Before going online:

1. Edit `deploy/Caddyfile` and replace `git-ci.example.com` with your real domain.
2. Ensure DNS points to this host and port 80/443 are reachable.
3. Confirm:
   ```bash
   curl -I http://127.0.0.1
   curl -kI https://127.0.0.1
   ```
4. Check in browser using the real domain.

5. Keep public route and health probe in sync:
   - DNS/edge should point this domain to the target host running this deployment.
   - If your origin is proxied (Cloudflare), ensure origin TLS mode and certificate strategy match your Caddy setup.
   - Quick probe:
     ```bash
     curl -s -I https://git-ci.example.com/health
     ```

You should see:

```
HTTP/2 202
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
   - map `git-ci.example.com` to `127.0.0.1:8080`
   - keep `/health` route returning `202` (copy from `Caddyfile.example`)
   - if you already have a shared host Caddyfile, you can copy `Caddyfile.host-snippet`
     and paste it there.
4. Validate:
   ```bash
   systemctl is-active git-ci-site
   curl -fsS http://127.0.0.1:8080/ >/dev/null
   ```

### Quick 525 troubleshooting

Cloudflare `525` usually means origin TLS/HTTPS handshake failure.

From the server (or another host inside your network), run:

```bash
openssl s_client -connect <origin-ip>:443 -servername git-ci.example.com < /dev/null
```

If the output includes `no peer certificate available`, the host Caddy config is
missing a matching TLS cert for that hostname or is not matching it on that host
block. Add the block from `deploy/Caddyfile.host-snippet` and reload/restart Caddy.

## Safety / hygiene

- This repo is public. Do not store credentials, API keys, tokens, real host credentials, or TLS material here.
- Use `Caddyfile.example` only as a template; keep real values local when deploying.
