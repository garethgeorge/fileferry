# fileferry

A single-binary, self-hosted file sharing service. No database, no external
services — just a binary and a data directory.

> **Status:** work in progress, not yet ready for widespread use.

![fileferry web UI](preview.jpg)

## Features

- **Instant share links** — the URL is returned the moment an upload starts.
  Downloaders tail-follow uploads in progress, so large files can be shared
  before the transfer finishes.
- **Drop, paste, or type** — drag files into the dropzone, paste text or a
  screenshot anywhere on the page, or type a snippet inline.
- **Optional client-side encryption** — AES-256-GCM with a random key generated
  in the browser and kept only in the link's `#fragment`. The key never reaches
  the server, and the filename is sealed in the ciphertext.
- **Rich previews** — syntax-highlighted text
  ([chroma](https://github.com/alecthomas/chroma)), rendered Markdown, native
  players for images/audio/video, and file listings for zip/tar archives. Add
  `?raw=1` for raw bytes or `?dl=1` to force a download.
- **Expiring uploads** — 1 day / 1 week / 1 month / 1 year / never, per upload
  (default 1 year). An hourly job removes expired files.

## Installation

The recommended setup is Docker Compose. Images are published to `ghcr.io` on
each release.

```yaml
# docker-compose.yml
services:
  fileferry:
    image: ghcr.io/garethgeorge/fileferry:latest
    ports:
      - "8080:8080"
    volumes:
      - fileferry-data:/data
    environment:
      FILEFERRY_DATA_DIR: /data
      FILEFERRY_BASE_URL: https://files.example.com
      FILEFERRY_MAX_SIZE: "10737418240" # 10 GiB
    restart: unless-stopped

volumes:
  fileferry-data:
```

```sh
docker compose up -d
```

The UI is served at `/upload/` — with the config above, `http://localhost:8080/upload/`.
Set `FILEFERRY_BASE_URL` to the address users reach the service at so share
links are correct.

### Without Compose

```sh
docker run -p 8080:8080 -v fileferry-data:/data ghcr.io/garethgeorge/fileferry:latest
```

### From source

```sh
go build -o fileferry .
./fileferry --addr :8080 --data-dir ./data
```

## Configuration

Every option is available as a flag or an environment variable. If both are
set, the flag wins; otherwise the env var is used, then the default.

| Flag                    | Env var                         | Default              | Meaning                                |
| ----------------------- | ------------------------------- | -------------------- | -------------------------------------- |
| `--addr`                | `FILEFERRY_ADDR`                | `:8080`              | listen address                         |
| `--data-dir`            | `FILEFERRY_DATA_DIR`            | `./data`             | where files are stored                 |
| `--base-url`            | `FILEFERRY_BASE_URL`            | derived from request | base URL used in returned share links  |
| `--max-size`            | `FILEFERRY_MAX_SIZE`            | 10 GiB               | maximum upload size in bytes           |
| `--default-expire-days` | `FILEFERRY_DEFAULT_EXPIRE_DAYS` | 365                  | default expiration in days (0 = never) |

## Authentication

fileferry has no built-in auth by design. Everything privileged lives under
`/upload/`; put an authenticating reverse proxy in front of that prefix.
Download URLs (`/<fileid>`) are public, but file IDs are unguessable. Have the
proxy set `X-Forwarded-Proto` and `X-Forwarded-Host` (or set `--base-url`) so
share links are correct.

## Development

```sh
just test         # go test -race ./...
just build
just css          # regenerate web/static/tailwind.css (needs the standalone tailwindcss CLI)
```

The web UI is static vanilla HTML/JS (`web/static/`), embedded with `go:embed`.
The generated Tailwind stylesheet is committed, so plain `go build` produces a
self-contained binary.
