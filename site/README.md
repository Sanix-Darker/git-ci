# git-ci.sanixdk.xyz dashboard files

Dashboard assets for the `gci` VPS service.

Files:

- `index.html` — dashboard shell, jobs list and run actions
- `styles.css` — dashboard styling
- `app.js` — API client for pipelines, jobs, runs, logs, retry, cancel, and metrics

Local preview:

```bash
cd /home/dk/github/git-ci/site
python3 -m http.server 4173
```

Open `http://127.0.0.1:4173`.

## Deployment notes

This directory is also embedded by `deploy/Dockerfile` and served by `gci serve`.

Health checks and endpoints:

```bash
GET /api/v1/health -> {"status":"ok"}
GET /api/v1/discover?workdir=.
GET /api/v1/runs
GET /api/v1/runs/{id}/logs?offset=0
GET /api/v1/webhooks
GET /api/v1/stack
POST /api/v1/webhook/github
POST /api/v1/webhook/gitlab
POST /api/v1/runs
```

Webhook history is also visible in the dashboard panel and is useful for replacing GitHub/GitLab
webhook triggers with a single VPS endpoint.
