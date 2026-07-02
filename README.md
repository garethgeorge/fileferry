# fileferry

NOTE: This project is a work in progress, not yet ready for widespread use.

A simple, single-binary, self-hosted filesharing service. No database, no external dependencies — just a binary and a data directory.

## Features

- **Share instantly**: the share URL is returned the moment an upload starts. Downloaders tail-follow in-progress uploads and receive chunks as they are written, so you can share large files without waiting for the upload to finish.
- **Paste or drop**: paste text (or screenshots) anywhere on the page, or drag files into the dropzone.
- **End-to-end-ish encryption**: optionally encrypt uploads with AES-256-GCM. A random key is generated in your browser and lives only in the share link's `#fragment` — it is never stored on the server, and the original filename is sealed inside the ciphertext so the URL leaks nothing about the content.
- **Rich previews**: text renders as syntax-highlighted pages ([chroma](https://github.com/alecthomas/chroma)), Markdown renders formatted, images/audio/video get native players, and zip/tar/tar.gz archives show their file listing. Append `?raw=1` for raw bytes, `?dl=1` to force a download.
- **Expiration**: uploads default to expiring after 1 year (choose 1 day / 1 week / 1 month / 1 year / never in the UI). A background job garbage-collects expired files hourly.
- **No database**: everything lives on the filesystem.

## Installation

### Docker (recommended)

Images are published to `ghcr.io` on each release.

```sh
docker run -p 8080:8080 -v fileferry-data:/data ghcr.io/garethgeorge/fileferry:latest
```

### Docker Compose

```yaml
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

### From source

```sh
go build -o fileferry .
./fileferry --addr :8080 --data-dir ./data
```

### Configuration

Every option can be set with a command-line flag or the matching environment
variable. If both are given, the flag wins; otherwise the env var is used, and
finally the default. Env vars are usually the easiest route for Docker.

| Flag                    | Env var                         | Default              | Meaning                               |
| ----------------------- | ------------------------------- | -------------------- | ------------------------------------- |
| `--addr`                | `FILEFERRY_ADDR`                | `:8080`              | listen address                        |
| `--data-dir`            | `FILEFERRY_DATA_DIR`            | `./data`             | where files are stored                |
| `--base-url`            | `FILEFERRY_BASE_URL`            | derived from request | base URL used in returned share links |
| `--max-size`            | `FILEFERRY_MAX_SIZE`            | 10 GiB               | maximum upload size in bytes          |
| `--default-expire-days` | `FILEFERRY_DEFAULT_EXPIRE_DAYS` | 365                  | default expiration in days (0 = never) |

## Authentication

fileferry has no built-in auth, by design. Everything privileged (the UI and
all APIs) lives under `/upload/`; put an authenticating reverse proxy in front
of that path prefix. Download URLs (`/<fileid>`) are public but file IDs are
unguessable. Have your reverse proxy set `X-Forwarded-Proto` and `X-Forwarded-Host`
(or set `--base-url`) so share links are correct.

## Storage Layout

Files are stored on disk in a simple directory structure, no database is used. Each file has a unique ID of the form `<day>-<nonce>[-<description>][.<ext>]`. The day is the number of days since 2000-01-01, base32-encoded and zero-padded to 3 digits (so IDs sort chronologically and keep a consistent width through ~2089), and the nonce is 6 random base32 characters. The optional description is a user-provided, human-readable slug. Because the day is derivable from the ID, a file's month directory is too — no lookup table needed.

```
data/
├── 2026-07/                      # year-month of the upload day
│   └── 9ef-p9m2rr-my-notes.txt   # <day>-<nonce>[-<description>].<ext>
├── inprogress/                   # uploads still streaming; renamed into the
│   └── 9ef-k2v9mm-big.iso        # month dir on completion, wiped on startup
└── expirations/
    └── 2027-07-02                # file IDs expiring on this date, one per line
```

The expirations folder contains a file for each date on which files are set to expire. Each file contains a list of file IDs that are scheduled to be deleted on that date. A background job runs hourly to check for expired files and delete them.

## Development

```sh
just test         # go test -race ./...
just build
just css          # regenerate web/static/tailwind.css (needs the standalone tailwindcss CLI)
```

The web UI is static vanilla HTML/JS (`web/static/`), embedded into the
binary with `go:embed`. The generated Tailwind stylesheet is committed, so
plain `go build` always produces a fully self-contained binary.
