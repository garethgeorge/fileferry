// ferryupload is a small, self-contained CLI for fileferry. It uploads a file
// or directory, the clipboard's contents (text, an image, or a copied
// file/folder), or stdin, and prints exactly the resulting share URL to stdout
// — everything else (progress, errors, notes) goes to stderr. Directories are
// compressed first: a .zip on Windows, a .tar.gz everywhere else.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"

	"golang.design/x/clipboard"
)

// version is stamped by goreleaser at release time.
var version = "dev"

// maxShortenLinkSize caps how much of the input --shortenlink will buffer to
// validate it's a single URL: generous for any realistic link, small enough
// to read into memory outright regardless of the input source.
const maxShortenLinkSize = 64 * 1024

func envStr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ferryupload: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	var (
		server           string
		apiKey           string
		encrypt          bool
		slug             string
		filenameOverride string
		useClipboard     bool
		shortenLink      bool
		quiet            bool
		showVersion      bool
		expireDays       int
		hasExpireDays    bool
	)

	flag.StringVar(&server, "server", envStr("FILEFERRY_SERVER", ""), "base URL of the fileferry server (env FILEFERRY_SERVER)")
	flag.StringVar(&apiKey, "api-key", envStr("FILEFERRY_API_KEY", ""), "bearer token for the server's /api (env FILEFERRY_API_KEY)")
	flag.BoolVar(&encrypt, "encrypt", false, "AES-256 encrypt the upload; the key rides in the URL fragment")
	flag.BoolVar(&encrypt, "e", false, "shorthand for --encrypt")
	flag.StringVar(&slug, "slug", "", "URL suffix, e.g. my-notes")
	flag.StringVar(&filenameOverride, "filename", "", "override the uploaded filename")
	flag.BoolVar(&useClipboard, "clipboard", false, "read input from (and write the result back to) the system clipboard: text, an image, or a copied file/folder")
	flag.BoolVar(&useClipboard, "c", false, "shorthand for --clipboard")
	flag.BoolVar(&shortenLink, "shortenlink", false, "treat the input as a single URL and upload it as a short link, served as a redirect instead of raw text")
	flag.BoolVar(&quiet, "quiet", false, "suppress the progress bar")
	flag.BoolVar(&quiet, "q", false, "shorthand for --quiet")
	flag.BoolVar(&showVersion, "version", false, "print the version and exit")
	expireFlag := func(s string) error {
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("invalid expire-days %q", s)
		}
		expireDays, hasExpireDays = n, true
		return nil
	}
	flag.Func("expire-days", "expiration in days, 0 = never (default: the server's configured default)", expireFlag)
	flag.Func("x", "shorthand for --expire-days", expireFlag)

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `ferryupload uploads a file or directory, the clipboard, or stdin to a
fileferry server and prints the resulting share URL. Directories are
compressed first (.zip on Windows, .tar.gz elsewhere).

Usage:
  ferryupload [flags] [path]
  echo "hello" | ferryupload [flags]

Flags:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Println("ferryupload " + version)
		return
	}

	if flag.NArg() > 1 {
		fatalf("too many arguments (expected at most one file path)")
	}
	path := flag.Arg(0)
	if useClipboard && path != "" {
		fmt.Fprintln(os.Stderr, "ferryupload: --clipboard set; ignoring the positional file argument")
		path = ""
	}

	if shortenLink && filenameOverride != "" {
		fatalf("--filename cannot be combined with --shortenlink")
	}

	if server == "" {
		fatalf("--server is required (or set FILEFERRY_SERVER)")
	}
	serverURL, err := url.Parse(server)
	if err != nil || serverURL.Host == "" {
		fatalf("invalid --server %q", server)
	}

	var (
		body     io.Reader
		size     int64 = -1
		filename string
		cleanup  func()
	)

	switch {
	case useClipboard:
		if err := clipboard.Init(); err != nil {
			fatalf("clipboard unavailable: %v", err)
		}
		if p, ok := clipboardFilePath(); ok {
			// A Finder/Explorer "Copy" of a file or folder: upload the actual
			// item (prepareFile archives it first if it's a directory).
			b, sz, name, clean, err := prepareFile(p, quiet)
			if err != nil {
				fatalf("clipboard file: %v", err)
			}
			body, size, filename, cleanup = b, sz, name, clean
		} else if text := clipboard.Read(clipboard.FmtText); len(text) > 0 {
			if existing := ownShareURL(string(text), serverURL); existing != "" {
				fmt.Fprintln(os.Stderr, "ferryupload: clipboard already holds a fileferry link, skipping upload")
				fmt.Println(existing)
				return
			}
			body = bytes.NewReader(text)
			size = int64(len(text))
			filename = "clipboard.txt"
		} else if data, name, ok := clipboardBinary(); ok {
			// A screenshot or other rich content (image, PDF, HTML, …).
			body = bytes.NewReader(data)
			size = int64(len(data))
			filename = name
		} else {
			fatalf("clipboard is empty or holds no content ferryupload can read")
		}
	case path != "":
		b, sz, name, clean, err := prepareFile(path, quiet)
		if err != nil {
			fatalf("%v", err)
		}
		body, size, filename, cleanup = b, sz, name, clean
	default:
		body = os.Stdin
		filename = "paste.txt"
	}
	if cleanup != nil {
		defer cleanup()
	}
	if filenameOverride != "" {
		filename = filenameOverride
	}

	// --shortenlink marks the input as a short-link redirect rather than a
	// document: like web/static/app.js's confirmShortLink flow, it uploads
	// with filename "link.link" so the server's ".link" preview (see
	// internal/preview/redirect.go) serves it as a redirect. That previewer
	// re-validates the content itself, but failing fast here catches a
	// mistaken --shortenlink (e.g. against a whole file) before spending a
	// round trip on it.
	if shortenLink {
		content, err := io.ReadAll(io.LimitReader(body, maxShortenLinkSize+1))
		if err != nil {
			fatalf("reading input for --shortenlink: %v", err)
		}
		if len(content) > maxShortenLinkSize {
			fatalf("--shortenlink input is too large to be a URL")
		}
		target, ok := isShortcutURL(content)
		if !ok {
			fatalf("--shortenlink input is not a single absolute http(s) URL")
		}
		body = strings.NewReader(target)
		size = int64(len(target))
		filename = "link.link"
	}

	var key string
	if encrypt {
		key, err = randomKey()
		if err != nil {
			fatalf("generating encryption key: %v", err)
		}
	}

	shareURL, err := upload(uploadParams{
		server:        serverURL,
		apiKey:        apiKey,
		body:          body,
		size:          size,
		filename:      filename,
		slug:          slug,
		expireDays:    expireDays,
		hasExpireDays: hasExpireDays,
		encryptKey:    key,
		quiet:         quiet,
	})
	if err != nil {
		fatalf("%v", err)
	}
	if encrypt {
		// base64url has no characters that need escaping in a URL fragment.
		shareURL += "#" + key
	}

	if useClipboard {
		clipboard.Write(clipboard.FmtText, []byte(shareURL))
		if runtime.GOOS == "linux" {
			fmt.Fprintln(os.Stderr, "ferryupload: note: on Linux/X11 the clipboard clears once this process exits unless a clipboard manager is running")
		}
	}

	fmt.Println(shareURL)
}

// randomKey returns a fresh 128-bit key, base64url-encoded without padding —
// exactly the scheme web/static/app.js's randomKey() uses, so links produced
// by either client decrypt the same way.
func randomKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// isShortcutURL reports whether content, once trimmed, is a single absolute
// http(s) URL with no embedded whitespace. It mirrors (duplicated by hand,
// like keyHeader in upload.go) the rule internal/preview/redirect.go uses to
// decide whether a ".link" file is safe to serve as a redirect.
func isShortcutURL(content []byte) (string, bool) {
	s := strings.TrimSpace(string(content))
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return s, true
}

// clipboardFilePath reports a single local file or directory path when the
// clipboard holds a file reference — a Finder/Explorer/file-manager "Copy" of an
// item rather than its contents. The per-platform lookup lives in clipboardFiles
// (macOS pasteboard file URLs, Windows CF_HDROP, freedesktop URI lists). When
// several items are copied, the first is used and the rest are noted on stderr.
func clipboardFilePath() (string, bool) {
	paths := clipboardFiles()
	if len(paths) == 0 {
		return "", false
	}
	if len(paths) > 1 {
		fmt.Fprintf(os.Stderr, "ferryupload: clipboard holds %d items; uploading only the first (%s)\n", len(paths), paths[0])
	}
	return paths[0], true
}

// fileURIToPath converts a single "file://" URI to a local filesystem path.
// Non-file schemes (e.g. http) return ok=false so a copied hyperlink is left for
// the text/short-link handling instead of being opened as a file. On macOS a
// pasteboard file URL is often a file-reference URL (file:///.file/id=…) that
// resolveFileURL turns into a real path; elsewhere url.Path already holds it
// (already percent-decoded, with an empty or "localhost" host).
func fileURIToPath(uri string) (string, bool) {
	uri = strings.TrimSpace(uri)
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return "", false
	}
	if p, ok := resolveFileURL(uri); ok {
		return p, true
	}
	return u.Path, true
}

// parseURIList extracts local file paths from a freedesktop URI list. With
// gnomeFormat the first line is an action word ("copy"/"cut", the
// x-special/gnome-copied-files convention) and is dropped. Comment lines (starting
// with '#') and non-file URIs are ignored.
func parseURIList(list string, gnomeFormat bool) []string {
	lines := strings.Split(list, "\n")
	if gnomeFormat && len(lines) > 0 {
		lines = lines[1:]
	}
	var paths []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if p, ok := fileURIToPath(line); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

// clipboardBinary returns the first non-text payload on the clipboard together
// with a filename whose extension is inferred from its format: a screenshot as
// clipboard.png, and any other rich type the library exposes (PDF, HTML, RTF, …)
// under a matching extension. Text is skipped so the caller's link-dedup still
// applies. It returns ok=false when there is no such content to read.
func clipboardBinary() (data []byte, filename string, ok bool) {
	for _, f := range clipboard.Formats() {
		if f == clipboard.FmtText {
			continue
		}
		if d := clipboard.Read(f); len(d) > 0 {
			return d, "clipboard" + clipboardExt(f), true
		}
	}
	return nil, "", false
}

// clipboardExt returns a filename extension (leading dot) for a clipboard
// format, preferring common, readable choices and falling back to the mime
// package, then ".bin" for anything unrecognized.
func clipboardExt(f clipboard.Format) string {
	if f == clipboard.FmtImage {
		// The clipboard library returns image data PNG-encoded on every
		// platform, so ".png" is always correct here.
		return ".png"
	}
	mimeType := f.MIME()
	if i := strings.IndexByte(mimeType, ';'); i >= 0 {
		mimeType = mimeType[:i] // drop parameters like ;charset=utf-8
	}
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "application/pdf":
		return ".pdf"
	case "text/html":
		return ".html"
	case "text/rtf", "application/rtf":
		return ".rtf"
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

// ownShareURL reports the trimmed text if it looks like a fileferry link this
// tool already produced against the same server (matched by host), so a
// repeat `--clipboard` run can skip re-uploading it instead of sharing a link
// to a link. This is a best-effort heuristic, not a security check.
func ownShareURL(text string, server *url.URL) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, " \t\n\r") {
		return ""
	}
	u, err := url.Parse(text)
	if err != nil || u.Host == "" || u.Path == "" || u.Path == "/" {
		return ""
	}
	if !strings.EqualFold(u.Host, server.Host) {
		return ""
	}
	return text
}
