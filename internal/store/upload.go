package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// claimTimeout is how long a created upload may sit unclaimed (no PUT) before
// it is aborted. Variable so tests can shorten it.
var claimTimeout = 2 * time.Minute

var errUploadAborted = errors.New("upload aborted")

// ActiveUpload is the shared state between one writer and any number of
// tailing readers. Readers wait on notify, which is closed and replaced under
// mu on every state change — unlike sync.Cond this composes with ctx.Done().
type ActiveUpload struct {
	id FileID

	mu      sync.Mutex
	size    int64 // bytes durably written to the in-progress file
	done    bool  // renamed to the final path
	err     error // upload failed; in-progress file removed
	claimed bool  // a writer has attached
	notify  chan struct{}
	timer   *time.Timer // claim timeout
}

// broadcast wakes all waiting readers. Caller must hold u.mu.
func (u *ActiveUpload) broadcast() {
	close(u.notify)
	u.notify = make(chan struct{})
}

func (u *ActiveUpload) state() (size int64, done bool, err error, notify <-chan struct{}) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.size, u.done, u.err, u.notify
}

// BeginUpload reserves the ID — regenerating the nonce on collision, so the
// returned FileID is authoritative — creates the in-progress file so readers
// can tail it immediately, registers the upload, and records the expiration
// (zero expiresAt means never). If no writer attaches within claimTimeout the
// upload is aborted.
func (s *Store) BeginUpload(id FileID, expiresAt time.Time) (FileID, error) {
	var f *os.File
	err := errors.New("no attempt")
	for try := 0; try < 4 && err != nil; try++ {
		if try > 0 {
			id.Nonce = newNonce()
		}
		if _, serr := os.Stat(s.finalPath(id)); serr == nil {
			err = fs.ErrExist
			continue
		}
		f, err = os.OpenFile(s.tempPath(id), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil && !errors.Is(err, fs.ErrExist) {
			return FileID{}, err
		}
	}
	if err != nil {
		return FileID{}, fmt.Errorf("could not reserve an id: %w", err)
	}
	f.Close() // the writer reopens it in AttachWriter

	u := &ActiveUpload{id: id, notify: make(chan struct{})}
	// Assign the timer under u.mu: its callback (and AttachWriter) read
	// u.timer under the same lock, and the callback can fire immediately.
	u.mu.Lock()
	u.timer = time.AfterFunc(claimTimeout, func() {
		s.finishUpload(u, false, errors.New("upload never started"), true)
	})
	u.mu.Unlock()
	s.mu.Lock()
	s.active[id.String()] = u
	s.mu.Unlock()

	if !expiresAt.IsZero() {
		if err := s.ScheduleExpiration(id, expiresAt); err != nil {
			s.abortUpload(u, err)
			return FileID{}, err
		}
	}
	return id, nil
}

// AttachWriter claims a previously created upload. Exactly one writer may
// claim an upload; later attempts get ErrConflict.
func (s *Store) AttachWriter(id FileID) (*UploadWriter, error) {
	s.mu.Lock()
	u := s.active[id.String()]
	s.mu.Unlock()
	if u == nil {
		return nil, ErrNotFound
	}
	u.mu.Lock()
	if u.claimed || u.done || u.err != nil {
		u.mu.Unlock()
		return nil, ErrConflict
	}
	u.claimed = true
	u.timer.Stop()
	u.mu.Unlock()

	f, err := os.OpenFile(s.tempPath(id), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		s.abortUpload(u, err)
		return nil, err
	}
	return &UploadWriter{s: s, u: u, f: f}, nil
}

// finishUpload moves an upload to its terminal state exactly once: commit
// renames the in-progress file into the month hierarchy, abort removes it and
// records cause. onlyIfUnclaimed makes it a no-op once a writer attached
// (claim-timeout path). The file operation happens under u.mu so readers
// observe file state and upload state consistently.
func (s *Store) finishUpload(u *ActiveUpload, commit bool, cause error, onlyIfUnclaimed bool) error {
	u.mu.Lock()
	if u.done || u.err != nil {
		err := u.err
		u.mu.Unlock()
		if commit {
			return err
		}
		return nil
	}
	if onlyIfUnclaimed && u.claimed {
		u.mu.Unlock()
		return nil
	}
	var err error
	if commit {
		if err = os.MkdirAll(filepath.Join(s.dataDir, u.id.MonthDir()), 0o755); err == nil {
			err = os.Rename(s.tempPath(u.id), s.finalPath(u.id))
		}
		if err != nil {
			u.err = err
			os.Remove(s.tempPath(u.id))
		} else {
			u.done = true
		}
	} else {
		os.Remove(s.tempPath(u.id))
		if cause == nil {
			cause = errUploadAborted
		}
		u.err = cause
	}
	u.timer.Stop()
	u.broadcast()
	u.mu.Unlock()

	s.mu.Lock()
	delete(s.active, u.id.String())
	s.mu.Unlock()
	return err
}

func (s *Store) abortUpload(u *ActiveUpload, cause error) {
	s.finishUpload(u, false, cause, false)
}

// UploadWriter streams an upload's bytes into the in-progress file, publishing
// progress to tailing readers. Invariant: size is incremented only after the
// bytes are in the file, so a reader that observes size can always ReadAt up
// to it.
type UploadWriter struct {
	s *Store
	u *ActiveUpload
	f *os.File
}

func (w *UploadWriter) Write(p []byte) (int, error) {
	w.u.mu.Lock()
	err := w.u.err
	w.u.mu.Unlock()
	if err != nil {
		return 0, err
	}
	n, werr := w.f.Write(p)
	if n > 0 {
		w.u.mu.Lock()
		w.u.size += int64(n)
		w.u.broadcast()
		w.u.mu.Unlock()
	}
	return n, werr
}

// Commit finalizes the upload: fsync, close, rename into place, and wake
// readers so they drain the remaining bytes and see EOF.
func (w *UploadWriter) Commit() error {
	if err := w.f.Sync(); err != nil {
		w.Abort(err)
		return err
	}
	if err := w.f.Close(); err != nil {
		w.Abort(err)
		return err
	}
	return w.s.finishUpload(w.u, true, nil, false)
}

// Abort fails the upload: the in-progress file is removed and tailing readers
// receive cause from their next Read.
func (w *UploadWriter) Abort(cause error) {
	w.f.Close()
	w.s.abortUpload(w.u, cause)
}

// TailReader streams a file that is still being uploaded, following new bytes
// as the writer appends them. It holds its own fd, so the Commit rename (and
// even an abort's unlink) cannot invalidate reads of already-published bytes.
type TailReader struct {
	u   *ActiveUpload
	f   *os.File
	off int64
	ctx context.Context
}

func (r *TailReader) Read(p []byte) (int, error) {
	for {
		size, done, uerr, notify := r.u.state()
		if r.off < size {
			n := size - r.off
			if max := int64(len(p)); n > max {
				n = max
			}
			m, err := r.f.ReadAt(p[:n], r.off)
			r.off += int64(m)
			if err == io.EOF && m > 0 {
				err = nil
			}
			return m, err
		}
		if uerr != nil {
			return 0, uerr
		}
		if done {
			return 0, io.EOF
		}
		select {
		case <-notify:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
}

func (r *TailReader) Close() error {
	return r.f.Close()
}
