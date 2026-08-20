# Service operations

`git-ci` remains a CLI tool. The same binary can also run as a durable CI/CD
service with an embedded landing page, operator console, API, scheduler,
execution worker, and SQLite database:

```text
Internet -> Caddy/TLS -> 127.0.0.1:8087 -> git-ci serve -> SQLite + workspaces
```

No external database, queue, frontend server, or scheduler is required.

## Quick start

Choose one or more parent directories containing trusted Git repositories:

```bash
install -d -m 0700 "$HOME/.local/state/gci"
git-ci serve \
  --listen 127.0.0.1:8087 \
  --state-dir "$HOME/.local/state/gci" \
  --projects-root /srv/projects
```

On the first start, the binary creates the state directory, SQLite schema, admin
token, session signing key, secret encryption key, and workspace directory. The
bootstrap token is printed once and stored in `admin.token`. Open `/login` and
authenticate with that token.

The service refuses non-loopback listen addresses. Put Caddy, nginx, HAProxy, or
another trusted TLS proxy in front of it rather than weakening this boundary.

## Runtime flags

| Flag | Environment | Default | Purpose |
| --- | --- | --- | --- |
| `--listen` | `GIT_CI_LISTEN` | `127.0.0.1:8087` | Loopback HTTP listener |
| `--state-dir` | `GIT_CI_STATE_DIR` | `.gci-service` | SQLite, keys, token, and workspaces |
| `--static-dir` | `GIT_CI_STATIC_DIR` | embedded | Optional public landing-page override |
| `--projects-root` | `GIT_CI_PROJECTS_ROOT` | CLI workdir | Repeatable selectable-project boundary |
| `--admin-token-file` | `GIT_CI_ADMIN_TOKEN_FILE` | `<state>/admin.token` | Admin bearer/login token |
| `--session-key-file` | `GIT_CI_SESSION_KEY_FILE` | `<state>/session.key` | Browser session signing key |
| `--session-ttl` | `GIT_CI_SESSION_TTL` | `8h` | Browser session lifetime |
| `--max-body-bytes` | `GIT_CI_MAX_BODY_BYTES` | `1048576` | JSON request size ceiling |

At least one project root is required. A registered project must resolve inside
an allowed root. Registering a path does not copy or modify that source checkout.
Each run gets a fresh immutable workspace pinned to its recorded commit.

## State layout

```text
<state-dir>/
  admin.token       # mode 0600
  session.key       # mode 0600
  secret.key        # mode 0600; required to decrypt stored secrets
  gci.db             # SQLite source of truth
  gci.db-wal         # transient while running
  gci.db-shm         # transient while running
  workspaces/        # isolated run checkouts
```

The state directory is forced to mode `0700`. Keep it on a local, durable disk.
Do not commit it, place it in a web root, or share it between concurrent service
processes. Losing `secret.key` makes encrypted project and environment secrets
unrecoverable. Losing `admin.token` removes the current operator credential.

## SQLite backup and restore

The simplest reliable backup is an offline snapshot of the complete state
directory. Stopping the service closes SQLite and checkpoints its WAL.

```bash
sudo systemctl stop git-ci
sudo tar --xattrs --acls -C /var/lib -czf \
  "/var/backups/gci-$(date -u +%Y%m%dT%H%M%SZ).tar.gz" gci
sudo systemctl start git-ci
curl -fsS http://127.0.0.1:8087/healthz
```

Restore only while the service is stopped. Restore the database and all three
credential files as one snapshot, preserve ownership and modes, then start the
service. Do not combine a database from one snapshot with keys from another.

Workspaces are disposable, but including them makes the backup procedure atomic
and uncomplicated. For large installations, they may be removed only after a
confirmed service stop; the worker recreates them as needed.

## Upgrade and application rollback

Release archives include the binary and a checksum manifest. Back up state
before every upgrade:

```bash
sha256sum --check git-ci_*_checksums.txt
sudo systemctl stop git-ci
sudo cp /usr/local/bin/git-ci /usr/local/bin/git-ci.previous
sudo install -m 0755 ./git-ci /usr/local/bin/git-ci
sudo systemctl start git-ci
curl -fsS http://127.0.0.1:8087/healthz
```

Schema initialization and migrations run when SQLite opens. If an upgrade must
be reverted after a migration, stop the service and restore both the previous
binary and the pre-upgrade state snapshot. A binary-only downgrade is not a safe
database rollback strategy.

## CI/CD execution model

The service discovers GitHub Actions and GitLab CI workflow files, normalizes
their job graph, and records immutable run, job, and step snapshots. Manual,
scheduled, and webhook runs all enter the same durable queue. Job and step replay
create new lineage-linked runs rather than mutating history.

GitHub Actions deployment metadata:

```yaml
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: production
    needs: [test]
    steps:
      - run: ./scripts/deploy.sh
    x-gci:
      rollback: ./scripts/rollback.sh
      verify: ./scripts/verify.sh
```

GitLab CI deployment metadata:

```yaml
deploy:
  stage: deploy
  environment:
    name: production
    deployment_tier: production
  script:
    - ./scripts/deploy.sh
  x-gci:
    rollback: ./scripts/rollback.sh
    verify: ./scripts/verify.sh
```

`x-gci.verify` requires `x-gci.rollback`, and rollback requires a deployment
environment. Rollback creates a new run pinned to the selected successful
deployment commit, executes the stored rollback command, then the optional
verification command. It never rewrites the source run.

Protected environments can require one approval before their deployment job is
leased. The current service has one admin identity, so this is an explicit
operator gate rather than multi-person separation of duties.

## Security boundary

`git-ci` executes repository-defined shell commands. Register only trusted
repositories and run the service as an unprivileged dedicated user. Project
roots are discovery allowlists, not sandboxes. A workflow receives a writable
workspace and can access every resource granted to the service account.

Project and environment secrets are encrypted at rest and redacted from stored
logs. Avoid passing secrets on command lines because child processes and tools
can still expose their own arguments or output.

The service currently has one administrator and no RBAC. It does not provide
tenant isolation, hosted runners, an artifact/cache backend, or SMTP delivery.
The Settings email-alert form is an interface preview only and does not send or
persist notification configuration. Third-party GitHub `uses:` actions are not
executed; local checkout semantics are supported and shell `run:` steps are the
portable path.

## Health and recovery

`GET /healthz` and `GET /health` are public liveness endpoints. All `/app` pages
require a browser session. All `/api/v1` resources require a session or bearer
token. Unknown API versions return `404`.

If startup fails, inspect service logs before modifying state:

```bash
journalctl -u git-ci -n 200 --no-pager
sudo -u gci /usr/local/bin/git-ci serve \
  --state-dir /var/lib/gci --projects-root /srv
```

Common causes are a public `--listen` value, no project root, unreadable project
paths, incorrect state ownership, a busy port, or missing key files after a
partial restore.
