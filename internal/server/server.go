// Package server wires fileferry's HTTP routes: the upload UI and API under
// /upload/ (auth is delegated to a reverse proxy on that prefix) and public
// file downloads at /{fileid}.
package server

import (
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
	mux.Handle("GET /upload/static/", http.StripPrefix("/upload/", http.FileServerFS(web.Static)))

	mux.HandleFunc("POST /upload/api/create", s.handleCreate)
	mux.HandleFunc("PUT /upload/api/put/{id}", s.handlePut)
	mux.HandleFunc("GET /upload/api/list", s.handleList)
	mux.HandleFunc("DELETE /upload/api/file/{id}", s.handleDelete)

	mux.HandleFunc("GET /{fileid}", s.handleDownload)

	return mux
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
