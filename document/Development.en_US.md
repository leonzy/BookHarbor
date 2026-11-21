# BookHarbor Developer Guide

[中文](Development.zh_CN.md) | English

This guide describes the current Go + Nuxt repository. The legacy `webserver/` Python/Tornado HTTP service has been removed. If a local `webserver/` directory remains, it contains only stale empty directories or caches; it is not a backend entry point and must not receive new backend code.

## Project layout

```text
BookHarbor/
  app/          # Nuxt 4 + Vue 3 + Vuetify + Pinia
  booksvr/      # The only Go HTTP backend and maintenance CLI
  tests/        # Image management, startup and signed-upgrade script tests
  conf/         # Nginx, Supervisor and upgrade trust configuration
  docker/       # Container startup, self-checks and status page
  resources/    # Reader templates, built-in sources and runtime assets
  scripts/      # Engineering, deployment, migration and reader tools
  spec/         # Current product rules
  design/       # Internal proposals and historical decisions
  document/     # User and developer documentation
```

See [CODE_WIKI](../CODE_WIKI.md) for the code map and [CONTRIBUTING](../CONTRIBUTING.md) for contribution and release rules.

## Recommended environment

- Linux or a working Docker Desktop setup;
- Docker Compose;
- Node.js 24;
- Go as declared in `booksvr/go.mod`;
- Python 3.11+ for image and engineering scripts only.

### 1. Start the real backend

From the repository root:

```bash
make dev
curl --fail --silent http://127.0.0.1:8080/api/welcome
```

`make dev` builds and starts the development image. The Compose file mounts the frontend directory, but Go source is not hot-reloaded. Rebuild and restart the development image after changing `booksvr/`; the old Python auto-restart instructions no longer apply.

Development data defaults to `/tmp/talebook-dev`. Do not point this isolated environment at your only production library.

### 2. Start Nuxt

```bash
cd app
npm ci
API_URL=http://127.0.0.1:8080 npm run dev -- --port 3000 --host 127.0.0.1
```

Open `http://127.0.0.1:3000/`. Confirm the real backend is available before starting Nuxt. A mock API is not an acceptable local product experience or real-data verification substitute.

If file watching fails, stop the server and investigate native filesystem events. Do not enable `CHOKIDAR_USEPOLLING`.

## Backend-only development

```bash
make build-go
make test-go
make lint-go
```

The binary is written to `booksvr/bin/booksvr`. Running `serve` directly requires an existing Calibre `metadata.db`, a user database, a cookie secret and other paths. The supported production assembly is defined by `Dockerfile`, `docker/start.sh` and Supervisor configuration. Legacy `server.py --syncdb` and `webserver/main.py` commands do not initialize the current application.

## Tests and quality gates

### Go

```bash
make test-go
make lint-go
```

Add `_test.go` coverage in the corresponding `booksvr/internal/<package>/` for every behavior change.

### Python image scripts

```bash
python3 -m pytest tests -q
make lint-py
```

The signed-upgrade executor requires root-owned trust configuration. Run its complete suite in a Linux root or project CI environment. A Windows collection error caused by missing `fcntl` is not evidence that the Linux suite passed.

### Frontend

```bash
cd app
npm ci
npm run lint
npx vitest run test/components test/composables test/plugins test/stores test/utils
npx vitest run -c test/upgrade.vitest.config.ts
npm run build
npx playwright install chromium
npx playwright test -c playwright.library.config.ts
```

Special Playwright configs cover audiobooks, covers and other pages. Most use short-lived mock APIs for isolated UI regression. Real login, files, permissions and reading still require Docker-backed verification.

### Documentation

```bash
make check-spec
make check-design
make check-i18n
```

Update `spec/` with every product behavior change. Material changes require one WIP single-file HTML proposal; record actual results before marking it ACTIVE. Historical proposals, research and commit-specific acceptance reports must not be rewritten as if they were current guides.

## Development boundaries

- Route frontend API calls through `app/plugins/talebook.js`.
- Keep Calibre library and BookHarbor user database handles distinct.
- Apply resource visibility checks to every new book, cover, download, media, OPDS or WebDAV entry point.
- Parse `auto.py` / `manual.py` as configuration data; no Python HTTP service executes them.
- Preserve `/data`, legacy API URLs, the Go module name, cookies, `TALEBOOK_*` variables and plugin protocol identifiers.
- Keep source, version, license and synchronization records for third-party reader assets.

## Troubleshooting

Startup state is exposed through `/status.json` and the status page. Useful commands:

```bash
docker compose -f docker-compose.dev.yml logs
docker compose -f docker-compose.dev.yml ps
```

Check `/data` permissions, PUID/PGID, Nginx configuration, database initialization/migration and `/data/log/talebook.log`. Do not look for the legacy `talebook:tornado-8000` process; the application process is Go `booksvr` plus explicit auxiliary workers.

When opening an issue, include the version/commit, deployment method, relevant startup state and redacted logs. Never attach production databases, secrets or private books.
