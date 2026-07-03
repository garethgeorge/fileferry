package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/garethgeorge/fileferry/internal/encrypt"
	"github.com/garethgeorge/fileferry/internal/preview"
	"github.com/garethgeorge/fileferry/internal/store"
	"github.com/garethgeorge/fileferry/web"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("fileid"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()

	// An encrypted file needs the key, which never travels in the URL path or
	// query. curl users pass it in a header; browsers get it from the URL
	// fragment via the bootstrap page, which copies it into a path-scoped
	// cookie and reloads. Either way, once the key is in hand the file flows
	// through the exact same preview/serving path as any other file.
	encrypted := id.Ext == encExt
	key := ""
	if encrypted {
		key = encryptionKey(r)
		if key == "" {
			// Serve the bootstrap page; its inline script reads the fragment,
			// sets the cookie, and redirects back here with the key attached.
			w.Header().Set("Content-Security-Policy", bootstrapCSP)
			http.ServeFileFS(w, r, web.Static, "static/decrypt.html")
			return
		}
	}

	res, err := s.store.Open(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		log.Printf("open %s: %v", id.String(), err)
		http.Error(w, "could not open file", http.StatusInternalServerError)
		return
	}
	defer res.Close()

	// Resolve the content to serve. A plaintext completed file is seekable, so
	// it supports range requests and repeatable preview reads. An encrypted
	// file is wrapped in a decrypting reader (validating the key up front, so a
	// wrong key is rejected before any bytes are written) with its real
	// extension recovered from the embedded filename; the decrypted stream is
	// not seekable, so it is served like an in-progress upload.
	ext := id.Ext
	filename := id.String()
	var seeker io.ReadSeeker
	var modTime time.Time
	var stream io.Reader
	var previewOpen func() (io.ReadCloser, error)

	switch {
	case encrypted:
		src := io.Reader(res.File)
		if res.File == nil {
			src = res.Tail
		}
		dr, derr := encrypt.NewReader(src, key)
		if errors.Is(derr, encrypt.ErrWrongKey) {
			http.Error(w, "Wrong key", http.StatusForbidden)
			return
		} else if derr != nil {
			log.Printf("decrypt %s: %v", id.String(), derr)
			http.Error(w, "could not decrypt file", http.StatusInternalServerError)
			return
		}
		if name := string(dr.Meta()); name != "" {
			filename = name
			ext = strings.TrimPrefix(filepath.Ext(name), ".")
		}
		stream = dr
		// Completed encrypted files can be previewed: the previewer reads the
		// decrypted stream (it consumes dr exactly once, and the preview and raw
		// branches are mutually exclusive, so sharing dr is safe).
		if res.File != nil {
			previewOpen = func() (io.ReadCloser, error) { return io.NopCloser(dr), nil }
		}
	case res.File != nil:
		seeker = res.File
		modTime = res.Info.ModTime()
		previewOpen = func() (io.ReadCloser, error) { return io.NopCloser(res.File), nil }
	default:
		stream = res.Tail
	}

	mimeType := preview.MimeTypeForExt(ext)

	// HTML preview for completed files (plaintext or decrypted), unless raw/dl
	// is forced. Encrypted preview subresources arrive with ?raw and fall
	// through to raw serving below, decrypting again with the cookie's key.
	if previewOpen != nil && !q.Has("raw") && !q.Has("dl") {
		pf := preview.File{
			ID:       id.String(),
			Ext:      ext,
			MimeType: mimeType,
			// For encrypted files the ciphertext size is a safe upper bound on
			// the plaintext size (used only to gate large-file previews).
			Size: res.Info.Size(),
			Open: previewOpen,
		}
		if p := s.previews.Find(pf); p != nil {
			if err := p.ServeHTTP(w, r, pf); err != nil {
				log.Printf("preview %s: %v", id.String(), err)
			}
			return
		}
	}

	// Raw serving, shared by plaintext and decrypted content. nosniff + a
	// sandboxing CSP defang uploaded HTML/SVG: content is served verbatim but
	// cannot run scripts on our origin. Inert types (PDF, images, audio, video)
	// are exempted from the sandbox because it blocks the browser's native
	// viewers — most visibly forcing PDFs and media to download instead of
	// rendering inline.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !sandboxExempt(mimeType) {
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	if q.Has("dl") {
		w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(filename)+`"`)
	}

	if seeker != nil {
		w.Header().Set("Content-Type", mimeType)
		http.ServeContent(w, r, "", modTime, seeker)
		return
	}
	// A plaintext in-progress tail is forced to text/plain so partial html/svg
	// cannot render; decrypted content keeps its real (recovered) type.
	s.streamContent(w, r, stream, mimeType, !encrypted)
}

// encryptionKey extracts the decryption key from the request: the header (used
// by API/curl clients) takes precedence over the browser's path-scoped cookie.
func encryptionKey(r *http.Request) string {
	if k := r.Header.Get(keyHeader); k != "" {
		return k
	}
	if c, err := r.Cookie(keyCookie); err == nil && c.Value != "" {
		if v, err := url.QueryUnescape(c.Value); err == nil {
			return v
		}
		return c.Value
	}
	return ""
}

// bootstrapCSP locks the bootstrap page down to its inline script and styles:
// no network, no external resources — it only paints a progress card, sets the
// cookie, and reloads. Inline styles are allowed so the page can render a
// styled "Decrypting…" state instead of raw unstyled text.
const bootstrapCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'"

// sandboxExempt reports whether raw content of this MIME type may be served
// without the sandboxing CSP. Only inert types the browser renders with a
// native viewer qualify (PDF, images, audio, video); SVG is excluded because
// it can execute script when navigated to as a document.
func sandboxExempt(mimeType string) bool {
	mt, _, _ := strings.Cut(mimeType, ";")
	mt = strings.TrimSpace(strings.ToLower(mt))
	switch {
	case mt == "application/pdf":
		return true
	case mt == "image/svg+xml":
		return false
	case strings.HasPrefix(mt, "image/"),
		strings.HasPrefix(mt, "video/"),
		strings.HasPrefix(mt, "audio/"):
		return true
	default:
		return false
	}
}

// sanitizeFilename strips characters that would break (or inject into) a
// Content-Disposition header value.
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == '/' {
			return '-'
		}
		return r
	}, name)
}

// streamContent serves a non-seekable reader as a chunked response, flushing as
// bytes arrive. forceTextForText downgrades text/* to text/plain (used for
// plaintext in-progress uploads so partial html/svg cannot render). If the read
// fails mid-stream the handler is aborted, severing the connection without a
// terminal chunk so the client sees a truncated transfer rather than a clean
// EOF.
func (s *Server) streamContent(w http.ResponseWriter, r *http.Request, src io.Reader, mimeType string, forceTextForText bool) {
	if forceTextForText && strings.HasPrefix(mimeType, "text/") {
		mimeType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", mimeType)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		switch {
		case err == nil:
		case errors.Is(err, io.EOF):
			return
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return // the downloader went away
		default:
			log.Printf("stream of %s failed: %v", r.URL.Path, err)
			panic(http.ErrAbortHandler)
		}
	}
}
