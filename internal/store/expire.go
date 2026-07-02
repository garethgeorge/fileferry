package store

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const expDateFormat = "2006-01-02"

// ScheduleExpiration appends id to the expiration index file for at's date.
// Appends are serialized by expMu; a single small line per file keeps them
// effectively atomic.
func (s *Store) ScheduleExpiration(id FileID, at time.Time) error {
	s.expMu.Lock()
	defer s.expMu.Unlock()
	name := filepath.Join(s.dataDir, expirationsDir, at.UTC().Format(expDateFormat))
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(id.String() + "\n")
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

// RunGC deletes every file listed in expiration index files dated on or
// before now. An index file is removed once all its entries are cleared; if
// some deletions fail it is rewritten with the survivors and retried on the
// next run. Missing files count as cleared (already deleted, or an upload
// that never completed).
func (s *Store) RunGC(now time.Time) error {
	today := now.UTC().Format(expDateFormat)
	dir := filepath.Join(s.dataDir, expirationsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			continue
		}
		if _, terr := time.Parse(expDateFormat, name); terr != nil {
			continue
		}
		// Names are YYYY-MM-DD, so string comparison is date comparison.
		if name > today {
			continue
		}
		if err := s.gcIndexFile(filepath.Join(dir, name)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func (s *Store) gcIndexFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var remaining []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		id, err := ParseID(line)
		if err != nil {
			continue // malformed entry: drop it
		}
		if rerr := os.Remove(s.finalPath(id)); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			remaining = append(remaining, line)
		}
	}
	scanErr := sc.Err()
	f.Close()
	if scanErr != nil {
		return scanErr
	}

	s.expMu.Lock()
	defer s.expMu.Unlock()
	if len(remaining) == 0 {
		return os.Remove(path)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(remaining, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return fmt.Errorf("%d entries could not be removed, kept for retry", len(remaining))
}
