<p align="center"><img src="app/public/brand/bookharbor.svg" width="96" alt="BookHarbor"></p>

# BookHarbor

[中文](README.md) | English

BookHarbor is a self-hosted ebook library with a Go HTTP backend and Nuxt 4 / Vue 3 frontend. It is independently maintained from [Talebook](https://github.com/talebook/talebook), preserving its commit history, copyright notices and BSD-2-Clause license.

Features include multiple source directories, indexing without copying books by default, background imports and ZIP support, source rename/move/delete synchronization with mount protection, directory browsing, file-size sorting, online reading, shelves, reading history, OPDS and device connectors. The interface supports Chinese, English and Russian, with light and dark themes.

## Run with Docker

```bash
git clone https://github.com/leonzy/BookHarbor.git
cd BookHarbor
docker compose up -d
```

Open `http://localhost:8080` and complete administrator setup. The main-branch image is `ghcr.io/leonzy/bookharbor:main`. Versioned images are available after a release.

For existing installations, keep the same `/data` bind mount and mount source books at `/books`, `/books1`, etc. The Calibre library remains at `/data/books/library`. Renaming the project does not migrate databases or copy source books. Existing custom site settings remain valid. The Compose service name `talebook`, `TALEBOOK_*` environment variables and internal protocol identifiers remain compatible. Back up your data and settings before changing images; only one application container may write to the data directory.

## Site name and logo

New installations use the BookHarbor name and logo. Administrators can edit the site title, header, and sidebar HTML in general settings; existing custom settings remain intact. Put custom sidebar images in `books/logo/` inside the persistent data directory and reference them as `/logo/filename` in sidebar HTML. This directory is `/data/books/logo/` inside the container, and image updates preserve existing files.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). `origin` is BookHarbor; configure `https://github.com/talebook/talebook.git` as `upstream` for selective updates. Calibre remains an external format tool; Python is used for image management and Calibre tools, not as an HTTP backend.

[Issues](https://github.com/leonzy/BookHarbor/issues) · [Releases](https://github.com/leonzy/BookHarbor/releases) · [Container images](https://github.com/leonzy/BookHarbor/pkgs/container/bookharbor)

## Attribution and license

Derived from Talebook. Original author and contributor copyright notices are retained in [LICENSE](LICENSE). Calibre, candle-reader and other dependencies remain independent projects. Third-party notices are in [document/licenses](document/licenses).
