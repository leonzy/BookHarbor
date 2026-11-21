# BookHarbor User Guide

[中文](README.zh_CN.md) | English

BookHarbor is a self-hosted ebook library for individuals and families. This guide covers common configuration and troubleshooting. The [domain specifications](../spec/索引.md) define complete product behavior; see the [developer guide](Development.en_US.md) for local development.

## Installation and first use

Docker Compose is the recommended installation method:

```bash
git clone https://github.com/leonzy/BookHarbor.git
cd BookHarbor
docker compose up -d
```

Open `http://localhost:8080` and complete administrator setup. Compose mounts `./data` at `/data`, including the library, user database, settings, logs and application-upgrade state.

Existing installations must reuse their original `/data` directory. Back up databases and settings before upgrading, and never run two containers against the same writable data directory. The rolling image is `ghcr.io/leonzy/bookharbor:main`; production deployments should prefer a verified release tag.

## Importing books

Configure source directories in the administration import settings. Host directories must be mounted explicitly, for example:

```yaml
services:
  talebook:
    volumes:
      - "${TALEBOOK_DATA_DIR:-./data}:/data"
      - "/srv/books:/books:ro"
      - "/srv/more-books:/books1:ro"
```

Enter `/books` and `/books1` in the import settings, not the host paths. Index mode registers original files without copying them. Copy and move imports are separate modes; understand their file effects and back up data before enabling them.

Scanning validates file and container structure and retains failure/skip reasons. A disconnected source mount must not cause books to be removed from an incomplete scan. Deleting a book in BookHarbor and deleting a source file are different operations; see the [library specification](../spec/书库.md) for trash and source synchronization rules.

## Users, private books and access scopes

- Account capabilities control reading, uploading, editing and administration.
- Private books are visible only to their collector and administrators; public books remain subject to guest policy and directory scopes.
- Administrators can limit browsing by directory, permission class, user and group.
- Covers, downloads, online reading, OPDS and WebDAV use the same resource authorization. Internal file paths are not a sharing mechanism.

After changing permissions, verify lists, details and downloads as an administrator, a regular user and a guest where applicable.

## Reading and devices

Readable formats depend on actual files, media type and backend reader capabilities. EPUB, MOBI/AZW/AZW3, PDF, TXT and comic containers open through their supported readers. Multiple editions and formats of a work can be selected on the detail page.

Kindle and other device delivery require a configured plugin/device connection. SMTP usernames are commonly full email addresses, and providers may require an application-specific password. Conversion and delivery run as background tasks; inspect task details and redacted logs after failures instead of submitting many duplicate jobs.

OPDS is available at `/opds/` and WebDAV at `/books/`. Add the configured prefix for subpath deployments. Login, account capabilities and book scopes still apply. The built-in MCP plugin lets authorized agents search and recommend books, read history, and confirm delivery or imports. See [`document/MCP.md`](MCP.md) for connection credentials, HTTP and stdio setup.

## Demo mode

Preserve the other values in `/data/books/settings/auto.py` and set the following in its `settings` data:

```python
"DEMO_MODE": True,
"DEMO_USERNAME": "demo",
```

After restart, only the configured demo account can sign in and the server rejects writes. The default demo password is `talebook`; protect any public deployment and use available configuration to change it. Demo mode cannot be disabled from the administration UI; update the configuration and restart to disable it.

Defaults now come from the Go backend, not the removed `webserver/settings.py` module.

## Site title and logo

Administrators can customize the site title, header and sidebar HTML. Put custom sidebar images in `books/logo/` inside the persistent data directory, or `/data/books/logo/` in the container, and reference them as `/logo/filename`.

Do not follow old instructions that mount images into `/var/www/talebook/app/dist/img/`; that is not the current persistent logo interface.

## Uploads and reverse proxies

The bundled Nginx configuration permits large uploads, and the frontend can use configurable chunked uploads. For HTTP `413`, inspect request-size limits and timeouts in an outer Nginx, Caddy, CDN or WAF.

A subpath deployment requires `TALEBOOK_BASE_PATH` at image build time; setting it only at runtime is insufficient. See the [reverse-proxy guide](reverse-proxy.md).

## Metadata and external services

Built-in plugins provide metadata, network libraries, annotations and device integrations. Enabled, configured and healthy are separate states. Enable the plugin, configure its connection, test it and only then run production tasks.

Administrators deploy any required external proxy, such as a Douban API proxy, and enter a URL reachable from the container. Never publish upstream cookies, account tokens or secrets in an issue. Plugin integration does not change the copyright or usage terms of external metadata and book files.

## Troubleshooting

1. Open `/status_page.html` or read `/status.json` to identify the startup phase and failed step.
2. Check the `/data` mount, free space, PUID/PGID and directory permissions.
3. Inspect `docker compose logs` and `/data/log/talebook.log`. The application process is Go `booksvr`, not the legacy `talebook:tornado-8000`.
4. If the page loads but APIs return 502, use startup state to determine whether the backend is still starting instead of repeatedly refreshing.
5. If a reader does not load, check format, permissions and the browser console, then temporarily disable extensions that may block same-origin scripts or resources.
6. Include the version/full commit, deployment method, reproduction steps and redacted logs in issues. Never attach production databases, passwords, cookies, secrets or private books.

See [application upgrades](application-upgrade.md) for signed in-place upgrades, persistence, rollback and trust configuration.
