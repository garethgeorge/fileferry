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
	"sync"
)

var (
	ErrNotFound = errors.New("file not found")
	ErrConflict = errors.New("upload already claimed")
)

const (
	expirationsDir = "expirations"
	// inprogressDir holds uploads still being written. Completed uploads are
	// renamed out of it into the month-based hierarchy; it is wiped on startup.
	inprogressDir = "inprogress"
)

type Store struct {
	dataDir string

	mu     sync.Mutex // guards active
	active map[string]*ActiveUpload

	expMu sync.Mutex // serializes expiration-index appends/rewrites
}

// New opens (creating if needed) the data directory. The inprogress directory
// is wiped and recreated: it runs before the server accepts requests, so every
// file it holds is an upload interrupted by a crash, and destroying them is the
// desired recovery.
func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, expirationsDir), 0o755); err != nil {
		return nil, err
	}
	inprogress := filepath.Join(dataDir, inprogressDir)
	if err := os.RemoveAll(inprogress); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(inprogress, 0o755); err != nil {
		return nil, err
	}
	return &Store{dataDir: dataDir, active: make(map[string]*ActiveUpload)}, nil
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

// tempPath is where an upload is written while in progress. The inprogress
// directory is flat; IDs are globally unique, so there are no collisions.
func (s *Store) tempPath(id FileID) string {
	return filepath.Join(s.dataDir, inprogressDir, id.String())
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
// the in-progress file (a miss there means we raced the rename), then the final
// path again.
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
