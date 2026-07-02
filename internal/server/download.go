package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/garethgeorge/fileferry/internal/preview"
	"github.com/garethgeorge/fileferry/internal/store"
)

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	id, err := store.ParseID(r.PathValue("fileid"))
	if err != nil {
		http.NotFound(w, r)
		return
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

	mimeType := preview.MimeTypeForExt(id.Ext)
	q := r.URL.Query()

	if res.File != nil && !q.Has("raw") && !q.Has("dl") {
		pf := preview.File{
			ID:       id.String(),
			Ext:      id.Ext,
			MimeType: mimeType,
			Size:     res.Info.Size(),
			Open: func() (io.ReadCloser, error) {
				return io.NopCloser(res.File), nil
			},
		}
		if p := s.previews.Find(pf); p != nil {
			if err := p.ServeHTTP(w, r, pf); err != nil {
				log.Printf("preview %s: %v", id.String(), err)
			}
			return
		}
	}

	// Raw serving. nosniff + a sandboxing CSP defang uploaded HTML/SVG:
	// content is served verbatim but cannot run scripts on our origin.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	if q.Has("dl") {
		w.Header().Set("Content-Disposition", `attachment; filename="`+id.String()+`"`)
	}

	if res.File != nil {
		w.Header().Set("Content-Type", mimeType)
		http.ServeContent(w, r, "", res.Info.ModTime(), res.File)
		return
	}
	s.streamTail(w, r, res.Tail, mimeType)
}

// streamTail serves an in-progress upload as a chunked response, flushing as
// bytes arrive so followers see them promptly. If the upload fails mid-stream
// the handler is aborted, severing the connection without a terminal chunk so
// the client sees a truncated transfer rather than a clean EOF.
func (s *Server) streamTail(w http.ResponseWriter, r *http.Request, tail *store.TailReader, mimeType string) {
	// The previewer never sees in-progress files; force plain text so partial
	// html/svg cannot render.
	if strings.HasPrefix(mimeType, "text/") {
		mimeType = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", mimeType)
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	buf := make([]byte, 32*1024)
	for {
		n, err := tail.Read(buf)
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
			log.Printf("tail of %s failed: %v", r.URL.Path, err)
			panic(http.ErrAbortHandler)
		}
	}
}
