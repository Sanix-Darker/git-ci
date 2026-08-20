# git-ci v0.5.0

`v0.5.0` introduces the durable, single-binary git-ci CI/CD service while
preserving the existing CLI workflow.

## Service

- `git-ci serve` runs the landing page, authenticated operator console, API v1,
  scheduler, webhook receiver, execution worker, and SQLite/WAL state.
- First start creates `gci.db`, authentication material, encrypted-secret key,
  and isolated workspaces under one mode-`0700` state directory.
- The HTTP listener is loopback-only by design and defaults to
  `127.0.0.1:8087`.
- Project discovery is constrained to configured VPS roots with search and
  autocomplete; registered projects no longer remain in candidate results.

## CI/CD control plane

- GitHub Actions and GitLab CI workflows normalize into immutable workflow,
  run, job, and step graphs.
- Manual, cron, and signed webhook triggers use the same durable run queue.
- Project and environment secrets are encrypted at rest and redacted in logs.
- Environment policies, approval gates, deployment records, and serialized
  deployment leases provide a lightweight CD control layer.
- Rollback creates a new commit-pinned run with explicit source/target lineage,
  stored rollback command, and optional verification command.
- Failed jobs and steps can be replayed. Successful-source replay requires
  confirmation, reruns dependency closure when required, and preserves lineage.

## Operator interface

- A short public landing page explains the VPS plus git-ci alternative to hosted
  CI services.
- The dark monochrome brutalist console includes project search, run filtering,
  time ranges, status histograms, pipeline graphs, logs, toasts, approvals,
  deployment rollback, and job/step replay controls.
- Desktop and mobile layouts use square controls, restrained status color, and
  reduced-motion support.
- Settings includes an email-alert configuration preview. SMTP delivery and
  persistence are intentionally deferred.

## API and deployment

- Authenticated APIs are versioned under `/api/v1`; removed unscoped replay
  aliases are not carried into the stable contract.
- Bearer clients and CSRF-protected browser sessions share the same API.
- New deployment examples run the actual binary rather than a static placeholder
  and keep Caddy adjacent to a loopback-bound service.
- Service operations and API examples are documented in
  [`SERVICE.md`](SERVICE.md) and [`API_V1.md`](API_V1.md).

## Upgrade notes

Back up the complete stopped state directory before replacing the binary. The
database schema is initialized and migrated at startup. Restore the pre-upgrade
state snapshot together with the prior binary if a downgrade is required.

The old static-site-only deployment examples denied `/app` and `/api`. Replace
those proxy rules so the application can enforce authentication on the complete
service surface. The canonical service port is `8087`.

## Current boundaries

- One administrator; no RBAC or multi-tenant isolation.
- Trusted local shell execution, not a hardened hosted-runner sandbox.
- No service artifact/cache backend and no SMTP notification delivery.
- Third-party GitHub `uses:` actions are not executed; prefer shell `run:` steps.
- Provider workflow syntax is a normalized practical subset, not full hosted
  GitHub Actions or GitLab Runner behavioral parity.
