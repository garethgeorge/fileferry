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
	"strconv"
	"syscall"
	"time"

	"github.com/garethgeorge/fileferry/internal/preview"
	"github.com/garethgeorge/fileferry/internal/server"
	"github.com/garethgeorge/fileferry/internal/store"
)

// version is stamped by goreleaser at release time.
var version = "dev"

// Every flag falls back to a FILEFERRY_-prefixed environment variable when not
// passed explicitly, so the service can be configured entirely through the
// environment (the norm for containers). Precedence: flag > env var > default.
func envStr(name, def string) string {
	if v, ok := os.LookupEnv(name); ok {
		return v
	}
	return def
}

func envInt64(name string, def int64) int64 {
	if v, ok := os.LookupEnv(name); ok {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			log.Fatalf("invalid %s=%q: %v", name, v, err)
		}
		return n
	}
	return def
}

func envInt(name string, def int) int {
	return int(envInt64(name, int64(def)))
}

func main() {
	addr := flag.String("addr", envStr("FILEFERRY_ADDR", ":8080"), "listen address")
	dataDir := flag.String("data-dir", envStr("FILEFERRY_DATA_DIR", "./data"), "directory for uploaded files")
	baseURL := flag.String("base-url", envStr("FILEFERRY_BASE_URL", ""), "base URL for share links (default: derived from the request)")
	maxSize := flag.Int64("max-size", envInt64("FILEFERRY_MAX_SIZE", 10<<30), "maximum upload size in bytes")
	defaultExpireDays := flag.Int("default-expire-days", envInt("FILEFERRY_DEFAULT_EXPIRE_DAYS", 365), "default expiration in days (0 = never)")
	flag.Parse()

	st, err := store.New(*dataDir)
	if err != nil {
		log.Fatalf("opening data dir: %v", err)
	}

	handler := server.New(st, preview.NewRegistry(preview.NewMarkdown(), preview.NewText(), preview.NewMedia(), preview.NewArchive()), server.Options{
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
