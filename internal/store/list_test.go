package store

import (
	"testing"
	"time"
)

// uploadNamed creates a completed named file in week (weeks since 2020-01-01).
func uploadNamed(t *testing.T, s *Store, week int, nonce, slug string) FileID {
	t.Helper()
	id := FileID{Week: week, Nonce: nonce, Slug: slug, Ext: "txt"}
	return mustUpload(t, s, id, time.Time{})
}

func TestListNewestFirstAcrossMonths(t *testing.T) {
	s := newTestStore(t)
	// Weeks 0 (2020-01) and 9 (2020-03) land in different month dirs.
	old := uploadNamed(t, s, 0, "aaaaaa", "old-file")
	newer := uploadNamed(t, s, 9, "bbbbbb", "new-file")
	mustUpload(t, s, FileID{Week: 9, Nonce: "cccccc", Ext: "txt"}, time.Time{}) // unnamed: hidden

	entries, next, err := s.List("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("unexpected nextCursor %q", next)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2 (unnamed files must be hidden)", len(entries))
	}
	if entries[0].ID != newer.String() || entries[1].ID != old.String() {
		t.Fatalf("wrong order: %s, %s", entries[0].ID, entries[1].ID)
	}
	if entries[0].Slug != "new-file" || entries[0].Ext != "txt" || entries[0].Size == 0 {
		t.Fatalf("bad entry: %+v", entries[0])
	}
}

func TestListPaginationAcrossMonthBoundary(t *testing.T) {
	s := newTestStore(t)
	var want []string
	// Newest first: week 9 entries sort before week 0 entries.
	for _, f := range []struct {
		week  int
		nonce string
	}{{9, "zzzzzz"}, {9, "mmmmmm"}, {9, "aaaaaa"}, {0, "zzzzzz"}, {0, "aaaaaa"}} {
		id := uploadNamed(t, s, f.week, f.nonce, "file")
		want = append(want, id.String())
	}

	var got []string
	cursor := ""
	pages := 0
	for {
		entries, next, err := s.List(cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			got = append(got, e.ID)
		}
		pages++
		if next == "" {
			break
		}
		cursor = next
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages != 3 {
		t.Fatalf("got %d pages, want 3", pages)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d: got %s, want %s", i, got[i], want[i])
		}
	}
}

func TestListSkipsInProgressUploads(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(FileID{Week: 9, Nonce: "pppppp", Slug: "half-done", Ext: "txt"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort(nil)
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}

	entries, _, err := s.List("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("in-progress upload leaked into listing: %+v", entries)
	}
}
