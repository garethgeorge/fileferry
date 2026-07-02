package store

import (
	"testing"
	"time"
)

// uploadNamed creates a completed named file for day (days since 2000-01-01).
func uploadNamed(t *testing.T, s *Store, day int, nonce, slug string) FileID {
	t.Helper()
	id := FileID{Day: day, Nonce: nonce, Slug: slug, Ext: "txt"}
	return mustUpload(t, s, id, time.Time{})
}

func TestListNewestFirstAcrossMonths(t *testing.T) {
	s := newTestStore(t)
	// Days 0 (2000-01) and 40 (2000-02) land in different month dirs.
	old := uploadNamed(t, s, 0, "aaaaaa", "old-file")
	newer := uploadNamed(t, s, 40, "bbbbbb", "new-file")
	// Unnamed uploads are listed too. Within day 40, "cccccc" sorts newest.
	unnamed := mustUpload(t, s, FileID{Day: 40, Nonce: "cccccc", Ext: "txt"}, time.Time{})

	entries, next, err := s.List("", 10)
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("unexpected nextCursor %q", next)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3 (all uploads, named and unnamed)", len(entries))
	}
	if entries[0].ID != unnamed.String() || entries[1].ID != newer.String() || entries[2].ID != old.String() {
		t.Fatalf("wrong order: %s, %s, %s", entries[0].ID, entries[1].ID, entries[2].ID)
	}
	if entries[0].Slug != "" || entries[0].Ext != "txt" || entries[0].Size == 0 {
		t.Fatalf("bad unnamed entry: %+v", entries[0])
	}
	if entries[1].Slug != "new-file" {
		t.Fatalf("bad named entry: %+v", entries[1])
	}
}

func TestListPaginationAcrossMonthBoundary(t *testing.T) {
	s := newTestStore(t)
	var want []string
	// Newest first: day 40 entries sort before day 0 entries.
	for _, f := range []struct {
		day   int
		nonce string
	}{{40, "zzzzzz"}, {40, "mmmmmm"}, {40, "aaaaaa"}, {0, "zzzzzz"}, {0, "aaaaaa"}} {
		id := uploadNamed(t, s, f.day, f.nonce, "file")
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
	id, err := s.BeginUpload(FileID{Day: 40, Nonce: "pppppp", Slug: "half-done", Ext: "txt"}, time.Time{})
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
