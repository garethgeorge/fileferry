package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func mustUpload(t *testing.T, s *Store, id FileID, expiresAt time.Time) FileID {
	t.Helper()
	id, err := s.BeginUpload(id, expiresAt)
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("content")); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGCRemovesDueFiles(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	due := mustUpload(t, s, testID("aaaaaa"), now.AddDate(0, 0, -1))
	future := mustUpload(t, s, testID("bbbbbb"), now.AddDate(0, 0, 1))
	never := mustUpload(t, s, testID("cccccc"), time.Time{})

	if err := s.RunGC(now); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Open(context.Background(), due); !errors.Is(err, ErrNotFound) {
		t.Fatalf("due file survived GC: %v", err)
	}
	for _, id := range []FileID{future, never} {
		if _, err := s.Open(context.Background(), id); err != nil {
			t.Fatalf("%s should have survived GC: %v", id.String(), err)
		}
	}

	// The due date's index file must be gone, the future one intact.
	expDir := filepath.Join(s.dataDir, expirationsDir)
	if _, err := os.Stat(filepath.Join(expDir, now.AddDate(0, 0, -1).Format(expDateFormat))); !os.IsNotExist(err) {
		t.Fatal("cleared expiration index file was not removed")
	}
	if _, err := os.Stat(filepath.Join(expDir, now.AddDate(0, 0, 1).Format(expDateFormat))); err != nil {
		t.Fatal("future expiration index file went missing")
	}
}

func TestGCToleratesAlreadyDeletedFiles(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)

	id := mustUpload(t, s, testID("dddddd"), now.AddDate(0, 0, -1))
	if err := s.Remove(id); err != nil {
		t.Fatal(err)
	}
	if err := s.RunGC(now); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(s.dataDir, expirationsDir, now.AddDate(0, 0, -1).Format(expDateFormat))
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("index file with only-missing entries was not removed")
	}
}

func TestGCSameDayExpires(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	id := mustUpload(t, s, testID("eeeeee"), now) // expires "today"
	if err := s.RunGC(now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(context.Background(), id); !errors.Is(err, ErrNotFound) {
		t.Fatal("file expiring today survived GC")
	}
}

func TestGCKeepsUnremovableEntries(t *testing.T) {
	s := newTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	gone := mustUpload(t, s, testID("ffffff"), now.AddDate(0, 0, -1))
	stuck := mustUpload(t, s, testID("gggggg"), now.AddDate(0, 0, -1))

	// `gone` is already deleted, so GC treats its entry as cleared. Then make
	// the month dir read-only so unlinking `stuck` fails.
	if err := s.Remove(gone); err != nil {
		t.Fatal(err)
	}
	month := filepath.Join(s.dataDir, stuck.MonthDir())
	if err := os.Chmod(month, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(month, 0o755)

	if err := s.RunGC(now); err == nil {
		t.Fatal("expected GC to report the stuck entry")
	}

	name := filepath.Join(s.dataDir, expirationsDir, now.AddDate(0, 0, -1).Format(expDateFormat))
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("index file should have been rewritten, not removed: %v", err)
	}
	if string(data) != stuck.String()+"\n" {
		t.Fatalf("rewritten index = %q, want just the stuck entry", data)
	}

	// After the permission problem clears, the retry succeeds.
	os.Chmod(month, 0o755)
	if err := s.RunGC(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("index file not removed after successful retry")
	}
}
