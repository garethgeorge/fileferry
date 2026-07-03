// Package server wires fileferry's HTTP routes: the admin UI under /upload/
// (meant to sit behind a reverse proxy; it injects a working API key into the
// page), the Bearer-authenticated upload API under /api/, and public file
// downloads at /{fileid}.
package server

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/garethgeorge/fileferry/internal/preview"
	"github.com/garethgeorge/fileferry/internal/store"
	"github.com/garethgeorge/fileferry/web"
)

type Options struct {
	// BaseURL, if set, prefixes returned share links (e.g. "https://share.example.com").
	// Otherwise links are derived from X-Forwarded-Proto/Host or the request Host.
	BaseURL string
	// MaxSize caps upload bodies in bytes.
	MaxSize int64
	// DefaultExpireDays applies when a create request omits expireDays.
	DefaultExpireDays int
	// APIKeys is the set of tokens accepted as a Bearer credential on /api/*.
	// It holds the operator-configured keys plus a per-process ephemeral key.
	APIKeys []string
	// WebUIKey is the ephemeral key handed to the browser (via /upload/config.js)
	// so the admin UI can talk to /api. It is also present in APIKeys.
	WebUIKey string
}

type Server struct {
	store    *store.Store
	previews *preview.Registry
	opts     Options
}

func New(st *store.Store, previews *preview.Registry, opts Options) http.Handler {
	s := &Server{store: st, previews: previews, opts: opts}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/upload/", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("GET /upload", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/upload/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /upload/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, web.Static, "static/index.html")
	})
	// config.js is rendered by the backend (not a static asset) so it can inject
	// the ephemeral web-UI key; it lives under /upload/, behind the admin proxy.
	mux.HandleFunc("GET /upload/config.js", s.handleConfigJS)
	mux.Handle("GET /upload/static/", http.StripPrefix("/upload/", http.FileServerFS(web.Static)))

	// The upload API is Bearer-authenticated so it can be exposed without the
	// proxy that gates the admin UI. Upload is a single request: the id/URL is
	// streamed back before the bytes finish (see handleUpload).
	mux.HandleFunc("POST /api/upload", s.requireAuth(s.handleUpload))
	mux.HandleFunc("GET /api/list", s.requireAuth(s.handleList))
	mux.HandleFunc("DELETE /api/file/{id}", s.requireAuth(s.handleDelete))

	mux.HandleFunc("GET /{fileid}", s.handleDownload)

	return mux
}

// handleConfigJS serves a tiny script that seeds the browser with the ephemeral
// API key. The key is hex, so it embeds safely in the JS string literal.
func (s *Server) handleConfigJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("window.FF_API_KEY = \"" + s.opts.WebUIKey + "\";\n"))
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header,
// or returns "" if the header is missing or not a bearer credential.
func bearerToken(h string) string {
	const prefix = "bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// validKey reports whether tok matches any configured key. It compares against
// every key with a constant-time compare and never short-circuits, so it does
// not leak (via timing) which key matched or how far the scan got.
func (s *Server) validKey(tok string) bool {
	if tok == "" {
		return false
	}
	var matched int
	for _, k := range s.opts.APIKeys {
		matched |= subtle.ConstantTimeCompare([]byte(tok), []byte(k))
	}
	return matched == 1
}

// requireAuth wraps a handler, rejecting requests without a valid Bearer key.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.validKey(bearerToken(r.Header.Get("Authorization"))) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) baseURL(r *http.Request) string {
	if s.opts.BaseURL != "" {
		return strings.TrimSuffix(s.opts.BaseURL, "/")
	}
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}
