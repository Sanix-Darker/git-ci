# git-ci.sanixdk.xyz landing page

Static site contents for `git-ci.sanixdk.xyz` style hosting.

Files:

- `index.html` — minimal project page with install commands and links
- `styles.css` — compact dark template used by the landing page

Local preview:

```bash
cd /home/dk/github/git-ci/site
python3 -m http.server 4173
```

Open `http://127.0.0.1:4173`.

## Deployment

The project now has a deployment scaffold in [`deploy/`](../deploy).

Health check endpoint:

```
GET /health -> HTTP 202
```
