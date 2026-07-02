package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
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

type createRequest struct {
	Filename string `json:"filename"`
	Slug     string `json:"slug"`
	// ExpireDays: nil means the server default, 0 means never.
	ExpireDays *int `json:"expireDays"`
	// Encrypt marks the upload for server-side AES encryption. The file is
	// stored with an ".encr" extension; the key travels in the ?key query
	// parameter on the PUT (and later the download) and is never stored.
	Encrypt bool `json:"encrypt"`
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
	// filenameHeader carries the (percent-encoded) original filename on an
	// encrypted upload; it is embedded in the ciphertext, never stored plainly.
	filenameHeader = "X-Filename"
	// maxFilenameLen caps the embedded filename, keeping the metadata frame
	// comfortably within a single chunk.
	maxFilenameLen = 1024
)

// decodeFilenameHeader percent-decodes the client-supplied filename and caps
// its length. It never errors: an undecodable value is used as-is.
func decodeFilenameHeader(v string) []byte {
	if v == "" {
		return nil
	}
	name := v
	if dec, err := url.QueryUnescape(v); err == nil {
		name = dec
	}
	if len(name) > maxFilenameLen {
		name = name[:maxFilenameLen]
	}
	return []byte(name)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	days := s.opts.DefaultExpireDays
	if req.ExpireDays != nil {
		days = *req.ExpireDays
	}
	if days < 0 || days > maxExpireDays {
		days = maxExpireDays
	}
	var expiresAt time.Time
	if days > 0 {
		expiresAt = time.Now().UTC().AddDate(0, 0, days)
	}

	// A custom extension in the URL suffix only overrides the file's own
	// extension for textual content, where relabeling is lossless.
	fileExt := strings.TrimPrefix(filepath.Ext(req.Filename), ".")
	contentIsText := strings.HasPrefix(preview.MimeTypeForExt(fileExt), "text/")

	id := store.NewID(time.Now(), req.Slug, req.Filename, contentIsText)
	if req.Encrypt {
		// Encrypted content is an opaque blob; the ".encr" extension marks it
		// and drops any preview-relabeling from the original extension.
		id.Ext = encExt
	}
	id, err := s.store.BeginUpload(id, expiresAt)
	if err != nil {
		log.Printf("create upload: %v", err)
		http.Error(w, "could not create upload", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{
		"id":  id.String(),
		"url": s.baseURL(r) + "/" + id.String(),
	})
}

func (s *Server) handlePut(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	uw, err := s.store.AttachWriter(id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, store.ErrConflict):
		http.Error(w, "upload already started", http.StatusConflict)
		return
	case err != nil:
		log.Printf("attach writer %s: %v", id.String(), err)
		http.Error(w, "could not start upload", http.StatusInternalServerError)
		return
	}

	// Encrypted uploads (".encr") are sealed on the way to disk. The key rides
	// in a header (kept out of the query string so it never lands in access
	// logs) and is never persisted; a missing key is a client error. The
	// original filename is embedded in the ciphertext so the download can
	// restore its content type without leaking it in the URL.
	dst := io.Writer(uw)
	var enc *encrypt.Writer
	if id.Ext == encExt {
		key := r.Header.Get(keyHeader)
		if key == "" {
			uw.Abort(errors.New("encryption key required"))
			http.Error(w, "encryption key required", http.StatusBadRequest)
			return
		}
		meta := decodeFilenameHeader(r.Header.Get(filenameHeader))
		enc, err = encrypt.NewWriter(uw, key, meta)
		if err != nil {
			uw.Abort(err)
			log.Printf("init encryption %s: %v", id.String(), err)
			http.Error(w, "could not start upload", http.StatusInternalServerError)
			return
		}
		dst = enc
	}

	// n counts plaintext bytes copied from the body (encryption happens in the
	// writer), so the Content-Length check below still compares like with like.
	n, err := io.Copy(dst, http.MaxBytesReader(w, r.Body, s.opts.MaxSize))
	if err != nil {
		uw.Abort(err)
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "upload failed", http.StatusBadRequest)
		return
	}
	if r.ContentLength > 0 && n != r.ContentLength {
		uw.Abort(errors.New("body shorter than Content-Length"))
		http.Error(w, "incomplete upload", http.StatusBadRequest)
		return
	}
	if enc != nil {
		if err := enc.Close(); err != nil {
			uw.Abort(err)
			log.Printf("finalize encryption %s: %v", id.String(), err)
			http.Error(w, "could not finalize upload", http.StatusInternalServerError)
			return
		}
	}
	if err := uw.Commit(); err != nil {
		log.Printf("commit %s: %v", id.String(), err)
		http.Error(w, "could not finalize upload", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
