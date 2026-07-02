// Package store implements fileferry's on-disk file storage: ID-addressed
// files in year-month directories, tail-followable in-progress uploads, an
// append-only expiration index, and listing.
package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrNotFound = errors.New("file not found")
	ErrConflict = errors.New("upload already claimed")
)

const expirationsDir = "expirations"

type Store struct {
	dataDir string

	mu     sync.Mutex // guards active
	active map[string]*ActiveUpload

	expMu sync.Mutex // serializes expiration-index appends/rewrites
}

// New opens (creating if needed) the data directory and removes orphaned .tmp
// files. It runs before the server accepts requests, so no active upload can
// own a .tmp yet — every one found is a crash leftover.
func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, expirationsDir), 0o755); err != nil {
		return nil, err
	}
	s := &Store{dataDir: dataDir, active: make(map[string]*ActiveUpload)}
	if err := s.sweepTemp(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) sweepTemp() error {
	dirs, err := os.ReadDir(s.dataDir)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if !d.IsDir() || !isMonthDir(d.Name()) {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(s.dataDir, d.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				os.Remove(filepath.Join(s.dataDir, d.Name(), e.Name()))
			}
		}
	}
	return nil
}

// isMonthDir reports whether name looks like "2026-07".
func isMonthDir(name string) bool {
	if len(name) != 7 || name[4] != '-' {
		return false
	}
	for i, c := range name {
		if i == 4 {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func (s *Store) finalPath(id FileID) string {
	return filepath.Join(s.dataDir, id.MonthDir(), id.String())
}

func (s *Store) tempPath(id FileID) string {
	return s.finalPath(id) + ".tmp"
}

// OpenResult is either a completed file (File+Info set, use http.ServeContent)
// or an in-progress upload (Tail set, stream chunked).
type OpenResult struct {
	File *os.File
	Info fs.FileInfo
	Tail *TailReader
}

func (res *OpenResult) Close() error {
	if res.File != nil {
		return res.File.Close()
	}
	return res.Tail.Close()
}

// Open resolves an ID to its content. The lookup order closes the race with a
// concurrent Commit rename: final path, then the active-upload registry, then
// the .tmp (a miss there means we raced the rename), then the final path again.
func (s *Store) Open(ctx context.Context, id FileID) (*OpenResult, error) {
	if res, err := s.openFinal(id); err == nil {
		return res, nil
	}
	s.mu.Lock()
	u := s.active[id.String()]
	s.mu.Unlock()
	if u != nil {
		if f, err := os.Open(s.tempPath(id)); err == nil {
			return &OpenResult{Tail: &TailReader{u: u, f: f, ctx: ctx}}, nil
		}
	}
	if res, err := s.openFinal(id); err == nil {
		return res, nil
	}
	return nil, ErrNotFound
}

func (s *Store) openFinal(id FileID) (*OpenResult, error) {
	f, err := os.Open(s.finalPath(id))
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	return &OpenResult{File: f, Info: info}, nil
}

// Remove deletes a file. If its upload is still in progress the upload is
// aborted, which removes the .tmp and terminates any tailing readers.
func (s *Store) Remove(id FileID) error {
	s.mu.Lock()
	u := s.active[id.String()]
	s.mu.Unlock()
	if u != nil {
		s.abortUpload(u, errors.New("file deleted during upload"))
		return nil
	}
	err := os.Remove(s.finalPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}
