# fileferry

A single-binary, self-hosted file sharing service. No database, no external
services — just a binary and a data directory.

> **Status:** work in progress, not yet ready for widespread use.

![fileferry web UI](preview.jpg)

## Features

- **Instant share links** — the share URL streams back in the upload response
  before the transfer finishes, and downloaders tail-follow uploads in progress,
  so large files can be shared while they're still being sent.
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
| `--api-key`             | `FILEFERRY_API_KEY`             | _(none)_             | comma-separated Bearer keys for `/api` (an ephemeral key for the web UI is always added) |

## Authentication

There are two privileged surfaces:

- **The upload API — `/api/*`** — is authenticated with a Bearer token. Every
  request must send `Authorization: Bearer <key>`, where `<key>` is any entry in
  `FILEFERRY_API_KEY` (a comma-separated list). This is the surface curl scripts,
  the macOS keybind below, and the web UI all post to; it can be exposed directly
  to the internet.
- **The admin UI — `/upload/`** — is the static web panel. It has no built-in auth,
  so put an authenticating reverse proxy in front of that prefix. On load it injects
  an **ephemeral** API key into the page (regenerated every time the server starts)
  so uploads from the browser just work — no key to paste. Because that key rotates
  on restart, use a value from `FILEFERRY_API_KEY` for anything that must persist
  (scripts, the keybind).

Download URLs (`/<fileid>`) are public, but file IDs are unguessable. Have the
proxy set `X-Forwarded-Proto` and `X-Forwarded-Host` (or set `--base-url`) so
share links are correct.

### Uploading from the terminal

Send the file with a single `POST`, streaming the bytes as the request body and
authenticating with a key from `FILEFERRY_API_KEY`:

```sh
KEY=your-api-key; BASE=https://files.example.com; F=./notes.txt
curl -sS -X POST "$BASE/api/upload?filename=$(basename "$F")" \
  -H "Authorization: Bearer $KEY" --data-binary @"$F" | jq -r .url
```

The raw response is `{"id":...,"url":...}`;
[`jq`](https://jqlang.github.io/jq/) is only needed here to pluck `.url` out of
that JSON.

### macOS: upload the clipboard with a keybind

Upload whatever is on the clipboard and replace it with the share URL — great for
screenshots. Save this as a script (fill in your own `KEY` and `BASE`). It uploads
an image if the clipboard holds one (via [`pngpaste`](https://github.com/jcsalterego/pngpaste),
`brew install pngpaste`), otherwise the clipboard text:

```sh
#!/bin/bash
KEY=your-api-key; BASE=https://files.example.com
if pngpaste - >/tmp/ff.png 2>/dev/null; then F=/tmp/ff.png; N=clip.png
else pbpaste >/tmp/ff.txt; F=/tmp/ff.txt; N=clip.txt; fi
URL=$(curl -sS -X POST "$BASE/api/upload?filename=$N" \
  -H "Authorization: Bearer $KEY" --data-binary @"$F" | jq -r .url)
printf '%s' "$URL" | pbcopy
```

Bind it to a hotkey natively:

1. **Automator → New Document → Quick Action.** Set "Workflow receives" to
   *no input*.
2. Add a **Run Shell Script** action and paste the script. Save it as e.g.
   "Upload clipboard".
3. **System Settings → Keyboard → Keyboard Shortcuts → Services**, find "Upload
   clipboard" under General, and assign a shortcut.

Now that shortcut uploads the clipboard and leaves the share URL ready to paste.
Deps: `jq`, plus `pngpaste` for image support (a text-only version needs only
`pbpaste`/`pbcopy`).

## Development

```sh
just test         # go test -race ./...
just build
just css          # regenerate web/static/tailwind.css (needs the standalone tailwindcss CLI)
```

The web UI is static vanilla HTML/JS (`web/static/`), embedded with `go:embed`.
The generated Tailwind stylesheet is committed, so plain `go build` produces a
self-contained binary.
