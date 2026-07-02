// fileferry is a single-binary self-hosted filesharing service. Files are
// shareable the moment an upload starts; see README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/garethgeorge/fileferry/internal/preview"
	"github.com/garethgeorge/fileferry/internal/server"
	"github.com/garethgeorge/fileferry/internal/store"
)

// version is stamped by goreleaser at release time.
var version = "dev"

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dataDir := flag.String("data-dir", "./data", "directory for uploaded files")
	baseURL := flag.String("base-url", "", "base URL for share links (default: derived from the request)")
	maxSize := flag.Int64("max-size", 10<<30, "maximum upload size in bytes")
	defaultExpireDays := flag.Int("default-expire-days", 365, "default expiration in days (0 = never)")
	flag.Parse()

	st, err := store.New(*dataDir)
	if err != nil {
		log.Fatalf("opening data dir: %v", err)
	}

	handler := server.New(st, preview.NewRegistry(preview.NewText()), server.Options{
		BaseURL:           *baseURL,
		MaxSize:           *maxSize,
		DefaultExpireDays: *defaultExpireDays,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Expiration GC: once at startup, then hourly. Hourly (not daily) so a
	// missed midnight after a sleep/restart is caught quickly; a run with
	// nothing due is one small readdir.
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			if err := st.RunGC(time.Now()); err != nil {
				log.Printf("expiration gc: %v", err)
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()

	srv := &http.Server{
		Addr:    *addr,
		Handler: handler,
		// No ReadTimeout: it would kill long-running large uploads.
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("fileferry %s listening on %s (data in %s)", version, *addr, *dataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down (waiting for in-flight uploads)")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
