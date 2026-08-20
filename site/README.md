# git-ci web surface

The public landing page is static. Login and the operator application are
server-rendered Go templates enhanced with a local copy of HTMX. Tailwind is a
build-only dependency; no Node.js process runs in production.

Run npm ci and npm run build:web to compile and copy the CSS and HTMX assets.
Run npm run test:e2e for the desktop operator journey and mobile responsive
coverage.

Routes:

- / is the public project page.
- /login creates a signed operator session.
- /app and /app/* are authenticated HTML routes.
- /api/v1 is the authenticated JSON control API.
- /healthz is the service health probe.
