# fileferry

NOTE: This project is a work in progress, not yet ready for widespread use.

A simple, single-binary, self-hosted filesharing service.

- **Share instantly**: the share URL is returned the moment an upload starts; downloaders tail-follow in-progress uploads and receive new chunks as they are written (and get cut off if the upload fails). Allows sharing large files without waiting for the upload to finish.
- **Paste or drop**: paste text (or screenshots) anywhere on the page, or drag files into the dropzone.
- **Previews**: text files render as syntax-highlighted pages ([chroma](https://github.com/alecthomas/chroma)); images, audio, and video are served with the right content type so the browser renders them natively. Append `?raw=1` for the raw bytes, `?dl=1` to force a download.
- **Expiration**: uploads default to expiring after 1 year (choose 1 day / 1 week / 1 month / 1 year / never in the UI). A background job garbage-collects expired files hourly.
- **No database**: everything lives on the filesystem.

## Usage

```sh
go build -o fileferry .
./fileferry --addr :8080 --data-dir ./data
```

Or with Docker (images are published to ghcr.io on each release):

```sh
docker run -p 8080:8080 -v fileferry-data:/data ghcr.io/garethgeorge/fileferry:latest
```

Flags:

| Flag                    | Default              | Meaning                               |
| ----------------------- | -------------------- | ------------------------------------- |
| `--addr`                | `:8080`              | listen address                        |
| `--data-dir`            | `./data`             | where files are stored                |
| `--base-url`            | derived from request | base URL used in returned share links |
| `--max-size`            | 10 GiB               | maximum upload size in bytes          |
| `--default-expire-days` | 365                  | default expiration (0 = never)        |

## Authentication

fileferry has no built-in auth, by design. Everything privileged (the UI and
all APIs) lives under `/upload/`; put an authenticating reverse proxy in front
of that path prefix. Download URLs (`/<fileid>`) are public but file IDs are 
unguessable. Have your reverse proxy set `X-Forwarded-Proto` and `X-Forwarded-Host` 
(or set `--base-url`) so share links are correct.

## Storage Layout

Files are stored on disk in a simple directory structure, no database is used. Each file has a unique ID, which is a base32-encoded string of the form `<week base32>-<nonce>[-<description>]`. The week is encoded as the number of weeks since the Unix epoch (1970-01-01), and the nonce is a random 5-byte value. The optional description is a user-provided string that can be used as a human readable identifier for the file.

```
data/
├── 2026-07/                      # year-month of the upload week
│   ├── ap-p9m2rr-my-notes.txt    # <week base32>-<nonce>[-<description>].<ext>
│   └── ap-k2v9mm-big.iso.tmp     # upload in progress (renamed when complete)
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
