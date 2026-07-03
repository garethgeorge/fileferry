# fileferry

A single-binary, self-hosted file sharing service. No database, no external
services — just a binary and a data directory.

> **Status:** work in progress, not yet ready for widespread use.

![fileferry web UI](preview.jpg)

## Features

- **Instant share links** — the share URL streams back before the transfer
  finishes, and downloaders tail-follow uploads still in progress.
- **Drop, paste, or type** — drag files into the dropzone, paste text or a
  screenshot, or type a snippet inline. The `ferryupload` CLI covers scripts
  and the clipboard.
- **URL shortener** — pastes containing only a URL are served as redirects.
- **Optional encryption** — AES-256-GCM with a random key kept only in the
  link's `#fragment`; the server never stores it.
- **Rich previews** — syntax-highlighted text, rendered Markdown, native
  image/audio/video players, and archive listings. Add `?raw=1` for raw bytes
  or `?dl=1` to force a download.
- **Expiring uploads** — 1 day / week / month / year / never, per upload.

## Quick start

Run the server, then grab the ephemeral API key it prints on startup (or set
your own via `FILEFERRY_API_KEY`, see Configuration):

```sh
docker run -p 8080:8080 -v fileferry-data:/data ghcr.io/garethgeorge/fileferry:latest
```

Install the CLI and upload something:

```sh
curl -fsSL https://raw.githubusercontent.com/garethgeorge/fileferry/main/install.sh | sh
FILEFERRY_SERVER=http://localhost:8080 FILEFERRY_API_KEY=<key> ferryupload notes.txt
```

The web UI is at `/upload/` — `http://localhost:8080/upload/` for the command
above.

## Installing

**CLI** — `ferryupload` (and `ferryserver`, if you'd rather run the server as
a bare binary than a container):

```sh
curl -fsSL https://raw.githubusercontent.com/garethgeorge/fileferry/main/install.sh | sh
curl -fsSL https://raw.githubusercontent.com/garethgeorge/fileferry/main/install.sh | sh -s -- ferryserver
```

Both are also plain binaries on the
[Releases page](https://github.com/garethgeorge/fileferry/releases)
(linux/darwin/windows, amd64/arm64). The server also ships as a Docker image
at `ghcr.io/garethgeorge/fileferry`.

### From source

```sh
go build -o ferryserver ./cmd/ferryserver
go build -o ferryupload ./cmd/ferryupload
```

## Using the CLI

```sh
export FILEFERRY_SERVER=https://files.example.com
export FILEFERRY_API_KEY=your-api-key

ferryupload notes.txt              # upload a file
echo "hello" | ferryupload         # upload stdin
ferryupload --clipboard            # upload the clipboard, replace it with the link
ferryupload --encrypt secret.pdf   # AES-256 encrypt; key rides in the #fragment
```

It prints exactly the share URL to stdout — everything else (progress,
errors) goes to stderr — so it composes cleanly in scripts. Run `ferryupload
--help` for the rest of the flags (expiry, slug, filename override, etc).

## Configuration

`ferryserver` takes every option as a flag or a `FILEFERRY_`-prefixed env var
(the flag wins if both are set):

| Flag                    | Env var                         | Default              | Meaning                                |
| ----------------------- | -------------------------------- | -------------------- | -------------------------------------- |
| `--addr`                | `FILEFERRY_ADDR`                | `:8080`              | listen address                         |
| `--data-dir`            | `FILEFERRY_DATA_DIR`            | `./data`             | where files are stored                 |
| `--base-url`            | `FILEFERRY_BASE_URL`            | derived from request | base URL used in returned share links  |
| `--max-size`            | `FILEFERRY_MAX_SIZE`            | 10 GiB               | maximum upload size in bytes           |
| `--default-expire-days` | `FILEFERRY_DEFAULT_EXPIRE_DAYS` | 365                  | default expiration in days (0 = never) |
| `--api-key`             | `FILEFERRY_API_KEY`             | _(none)_             | comma-separated Bearer keys for `/api` (an ephemeral key for the web UI is always added) |

Set `FILEFERRY_BASE_URL` to the address users reach the service at (or forward
`X-Forwarded-Proto`/`X-Forwarded-Host` through a proxy) so share links are
correct. The web UI at `/upload/` has no auth of its own — put an
authenticating reverse proxy in front of it if it shouldn't be public.
Download URLs (`/<fileid>`) are public but unguessable.

## Development

```sh
just test         # go test -race ./...
just build
just css          # regenerate web/static/tailwind.css (needs the standalone tailwindcss CLI)
```

The web UI is static vanilla HTML/JS (`web/static/`), embedded with
`go:embed`. The generated Tailwind stylesheet is committed, so plain `go
build` produces a self-contained binary.
