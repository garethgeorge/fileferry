package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/garethgeorge/fileferry/internal/encrypt"
	"github.com/garethgeorge/fileferry/internal/preview"
	"github.com/garethgeorge/fileferry/internal/store"
)

const maxExpireDays = 3650

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writing response: %v", err)
	}
}

const (
	// encExt is the extension used to mark an encrypted file.
	encExt = "encr"
	// keyHeader carries the encryption/decryption key. It is a header rather
	// than a query parameter so it never appears in access logs or Referer.
	keyHeader = "X-Encryption-Key"
	// keyCookie carries the decryption key on browser downloads: the bootstrap
	// page copies the key from the URL fragment into this path-scoped cookie,
	// which the browser then sends (in the Cookie header, not the query string)
	// on the navigation and every preview subresource.
	keyCookie = "ffkey"
	// maxFilenameLen caps the embedded filename, keeping the metadata frame
	// comfortably within a single chunk.
	maxFilenameLen = 1024
)

// filenameMeta returns the original filename to seal into an encrypted file's
// metadata frame, capped so it stays within a single chunk. The value arrives
// as a query parameter, already percent-decoded by the URL parser.
func filenameMeta(name string) []byte {
	if name == "" {
		return nil
	}
	if len(name) > maxFilenameLen {
		name = name[:maxFilenameLen]
	}
	return []byte(name)
}

// handleUpload creates a file and streams its bytes in a single request. It
// mints the id, flushes the {id,url} JSON to the client immediately — before a
// single byte of the body is read — and only then copies the body into the
// file. So a "dumb" client (curl) just waits and reads the URL from the
// response like any other JSON, while a streaming client can grab the URL and
// share it while the upload is still in flight (and downloaders tail-follow it).
//
// Because the 200 + id/url line is flushed up front, the status can no longer
// change once the body copy begins: everything that can fail cleanly (bad
// params, a missing encryption key) is checked first, and any failure *after*
// the flush aborts the connection so the client sees a truncated response.
//
// Metadata rides in the query string (filename, slug, expireDays, encrypt); the
// encryption key rides in a header so it never lands in an access log.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filename := q.Get("filename")
	slug := q.Get("slug")
	encryptOn := q.Get("encrypt") == "true" || q.Get("encrypt") == "1"

	// expireDays: absent means the server default, 0 means never.
	days := s.opts.DefaultExpireDays
	if v := q.Get("expireDays"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			http.Error(w, "invalid expireDays", http.StatusBadRequest)
			return
		}
		days = n
	}
	if days < 0 || days > maxExpireDays {
		days = maxExpireDays
	}
	var expiresAt time.Time
	if days > 0 {
		expiresAt = time.Now().UTC().AddDate(0, 0, days)
	}

	// Encrypted uploads need a key. Validate it now, while we can still return a
	// clean 400 (once the id/url is flushed the status is locked in).
	var key string
	if encryptOn {
		if key = r.Header.Get(keyHeader); key == "" {
			http.Error(w, "encryption key required", http.StatusBadRequest)
			return
		}
	}

	// A custom extension in the URL suffix only overrides the file's own
	// extension for textual content, where relabeling is lossless.
	fileExt := strings.TrimPrefix(filepath.Ext(filename), ".")
	contentIsText := strings.HasPrefix(preview.MimeTypeForExt(fileExt), "text/")
	id := store.NewID(time.Now(), slug, filename, contentIsText)
	if encryptOn {
		// Encrypted content is an opaque blob; the ".encr" extension marks it
		// and drops any preview-relabeling from the original extension.
		id.Ext = encExt
	}

	id, err := s.store.BeginUpload(id, expiresAt)
	if err != nil {
		log.Printf("begin upload: %v", err)
		http.Error(w, "could not create upload", http.StatusInternalServerError)
		return
	}
	uw, err := s.store.AttachWriter(id)
	if err != nil {
		// We registered this upload a line ago and are the only writer, so a
		// failure here is unexpected rather than a conflict.
		log.Printf("attach writer %s: %v", id.String(), err)
		http.Error(w, "could not start upload", http.StatusInternalServerError)
		return
	}

	// Build the encryptor (if any) before responding: the key is embedded with
	// the original filename so the ".encr" URL leaks no content type, and a
	// setup failure can still be a clean 500 here.
	dst := io.Writer(uw)
	var enc *encrypt.Writer
	if encryptOn {
		enc, err = encrypt.NewWriter(uw, key, filenameMeta(filename))
		if err != nil {
			uw.Abort(err)
			log.Printf("init encryption %s: %v", id.String(), err)
			http.Error(w, "could not start upload", http.StatusInternalServerError)
			return
		}
		dst = enc
	}

	// Flush the id/url now, before reading the body. json.Encoder appends a
	// newline, so a streaming client can read exactly one line and parse it.
	idStr := id.String()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Stream the id/url now, before the body is read. Go's HTTP/1 server would
	// otherwise drain the (still-uploading) request body on the first flush to
	// keep the connection alive; Connection: close skips that drain so the flush
	// lands immediately. The upload owns the connection anyway, so closing it
	// afterward costs nothing.
	w.Header().Set("Connection", "close")
	if err := json.NewEncoder(w).Encode(map[string]string{
		"id":  idStr,
		"url": s.baseURL(r) + "/" + idStr,
	}); err != nil {
		uw.Abort(err)
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	// From here the status is committed; any error severs the connection so the
	// client sees a truncated (failed) response. n counts plaintext bytes, so
	// the Content-Length check still compares like with like when encrypting.
	n, err := io.Copy(dst, http.MaxBytesReader(w, r.Body, s.opts.MaxSize))
	if err != nil {
		uw.Abort(err)
		log.Printf("upload %s: %v", idStr, err)
		panic(http.ErrAbortHandler)
	}
	if r.ContentLength > 0 && n != r.ContentLength {
		uw.Abort(errors.New("body shorter than Content-Length"))
		panic(http.ErrAbortHandler)
	}
	if enc != nil {
		if err := enc.Close(); err != nil {
			uw.Abort(err)
			log.Printf("finalize encryption %s: %v", idStr, err)
			panic(http.ErrAbortHandler)
		}
	}
	if err := uw.Commit(); err != nil {
		// Commit aborts the upload on failure internally.
		log.Printf("commit %s: %v", idStr, err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	entries, next, err := s.store.List(r.URL.Query().Get("cursor"), limit)
	if err != nil {
		log.Printf("list: %v", err)
		http.Error(w, "could not list files", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []store.ListEntry{}
	}
	writeJSON(w, map[string]any{"entries": entries, "nextCursor": next})
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch err := s.store.Remove(id); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		log.Printf("delete %s: %v", id.String(), err)
		http.Error(w, "could not delete file", http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
