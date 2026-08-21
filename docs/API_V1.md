# HTTP API v1

The service API is rooted at `/api/v1`. A future breaking contract will use a
new major path; unversioned and removed aliases are not compatibility surfaces.
`GET /api/v1` returns the API identifier and capability document. The running
service version is exposed by `GET /healthz`.

The execution graph exposes durable `step-summaries` and `step-annotations`.
Workers advertise `workflow-commands` support for GitHub-compatible `add-mask`,
`stop-commands`, `notice`, `warning`, `error`, `group`, and `endgroup` stdout
commands. Run-log responses expose `log-sections` for GitHub groups and GitLab
collapsible section markers without changing the ordered `items` collection.

## Authentication

For scripts, use the admin token as a bearer credential:

```bash
export GCI_URL=https://gci.example.com
export GCI_TOKEN="$(sudo cat /var/lib/gci/admin.token)"
curl --fail-with-body \
  --header "Authorization: Bearer ${GCI_TOKEN}" \
  "${GCI_URL}/api/v1"
```

Bearer-authenticated mutations do not require CSRF. Browser clients authenticate
through `POST /api/v1/session/login`; its response sets the session cookie and
returns `csrfToken`. Send that value as `X-CSRF-Token` for `POST`, `PATCH`, and
`DELETE` requests made with the cookie. Login and webhook request bodies are
limited by `--max-body-bytes` like other JSON endpoints.

Never place the token in a query string, workflow file, image, or repository.

## Resource map

| Method | Route | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/session/login` | Exchange admin token for browser session |
| `GET`, `DELETE` | `/api/v1/session` | Inspect or end a session |
| `GET` | `/api/v1/project-candidates` | Search unregistered repositories in allowed roots |
| `GET`, `POST` | `/api/v1/projects` | List or register projects |
| `GET` | `/api/v1/projects/{project}` | Project detail |
| `GET` | `/api/v1/projects/{project}/workflows` | List discovered workflows |
| `POST` | `/api/v1/projects/{project}/workflows/sync` | Re-read workflow files |
| `GET` | `/api/v1/workflows/{workflow}` | Workflow graph |
| `POST` | `/api/v1/workflows/{workflow}/runs` | Queue a run |
| `GET` | `/api/v1/projects/{project}/runs` | List project runs |
| `GET` | `/api/v1/runs/{run}` | Immutable run/job/step graph and lineage |
| `GET` | `/api/v1/runs/{run}/logs` | Redacted run logs |
| `POST` | `/api/v1/runs/{run}/cancel` | Request cancellation |
| `GET` | `/api/v1/runs/{run}/replay-options` | Eligible jobs and steps with closure/gate disclosure |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/replays` | Queue a job replay |
| `POST` | `/api/v1/runs/{run}/jobs/{job}/steps/{step}/replays` | Queue a step replay |
| `GET`, `POST` | `/api/v1/projects/{project}/secrets` | List metadata or create project secret |
| `DELETE` | `/api/v1/secrets/{secret}` | Delete project secret |
| `GET`, `POST` | `/api/v1/projects/{project}/schedules` | List or create cron schedules |
| `PATCH`, `DELETE` | `/api/v1/schedules/{schedule}` | Update or delete schedule |
| `GET`, `POST` | `/api/v1/projects/{project}/webhooks` | List or create webhook endpoints |
| `GET`, `POST` | `/api/v1/projects/{project}/environments` | List or create environment policy |
| `GET`, `PATCH` | `/api/v1/environments/{environment}` | Inspect or update environment policy |
| `GET`, `POST` | `/api/v1/environments/{environment}/secrets` | Environment secret metadata/create |
| `DELETE` | `/api/v1/environment-secrets/{secret}` | Delete environment secret |
| `GET` | `/api/v1/approvals` | Filter approval requests |
| `GET` | `/api/v1/approvals/{approval}` | Approval detail |
| `POST` | `/api/v1/approvals/{approval}/decision` | Approve or reject a gate |
| `GET`, `POST` | `/api/v1/projects/{project}/deployments` | List or record deployments |
| `GET`, `PATCH` | `/api/v1/deployments/{deployment}` | Detail or transition deployment |
| `GET` | `/api/v1/deployments/{deployment}/rollback-options` | Eligible rollback targets |
| `POST` | `/api/v1/deployments/{deployment}/rollback` | Queue provenance-preserving rollback |

Webhook delivery uses the generated `/hooks/{endpoint}` path and provider
signature, not the admin bearer token.

## Register, sync, and run

Register a repository path under an allowed project root:

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"slug":"api","name":"API","path":"/srv/projects/api","defaultBranch":"main"}' \
  "${GCI_URL}/api/v1/projects"
```

Use the returned project ID to sync workflows, then the workflow ID to queue a
specific ref:

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  "${GCI_URL}/api/v1/projects/${PROJECT_ID}/workflows/sync"

curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"ref":"refs/heads/main"}' \
  "${GCI_URL}/api/v1/workflows/${WORKFLOW_ID}/runs"
```

The queued run records its resolved commit and source path before execution.

## Replay

Inspect eligibility first. The response discloses dependency closure, whether a
successful source needs explicit confirmation, and whether a protected
environment can introduce an approval gate.

```bash
curl --fail-with-body \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  "${GCI_URL}/api/v1/runs/${RUN_ID}/replay-options"
```

Queue a job replay with a unique idempotency key:

```bash
curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: replay-${RUN_ID}-${JOB_ID}-1" \
  -d '{"confirmSuccessful":true}' \
  "${GCI_URL}/api/v1/runs/${RUN_ID}/jobs/${JOB_ID}/replays"
```

For a step replay, append `/steps/${STEP_ID}/replays` after the job segment. The
service validates that the run owns the job and the job owns the step. Reusing
the same key returns the original replay; a second active replay with a different
key conflicts. The response contains the new run and lineage record.

## Approvals and rollback

List pending approvals for a project and make an explicit decision:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  "${GCI_URL}/api/v1/approvals?projectId=${PROJECT_ID}&status=pending"

curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  -H 'Content-Type: application/json' \
  -d '{"decision":"approved","reason":"release window"}' \
  "${GCI_URL}/api/v1/approvals/${APPROVAL_ID}/decision"
```

Rollback also requires an eligibility query and idempotency key:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  "${GCI_URL}/api/v1/deployments/${SOURCE_DEPLOYMENT_ID}/rollback-options"

curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${GCI_TOKEN}" \
  -H 'Content-Type: application/json' \
  -H "Idempotency-Key: rollback-${SOURCE_DEPLOYMENT_ID}-1" \
  -d "{\"targetDeploymentId\":\"${TARGET_DEPLOYMENT_ID}\"}" \
  "${GCI_URL}/api/v1/deployments/${SOURCE_DEPLOYMENT_ID}/rollback"
```

Rollback targets are restricted to eligible successful deployments in the same
project, workflow, and environment lineage. A rollback is a new auditable run;
it does not alter deployment or run history.
