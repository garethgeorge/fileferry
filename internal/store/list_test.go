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
	// Unnamed uploads are listed too. It's uploaded last, so by mtime it
	// sorts newest among day 40's entries, ahead of "newer".
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
	// Upload order (also mtime order): day 40's zzzzzz, mmmmmm, aaaaaa, then
	// day 0's zzzzzz, aaaaaa. Newest first means day 40 sorts before day 0,
	// and within a day, most-recently-uploaded (by mtime) sorts first — the
	// reverse of upload order, not the nonces' alphabetical order.
	var uploaded []string
	for _, f := range []struct {
		day   int
		nonce string
	}{{40, "zzzzzz"}, {40, "mmmmmm"}, {40, "aaaaaa"}, {0, "zzzzzz"}, {0, "aaaaaa"}} {
		id := uploadNamed(t, s, f.day, f.nonce, "file")
		uploaded = append(uploaded, id.String())
	}
	want := []string{uploaded[2], uploaded[1], uploaded[0], uploaded[4], uploaded[3]}

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

func TestListCursorSurvivesDeletedFile(t *testing.T) {
	s := newTestStore(t)
	var uploaded []string
	for _, nonce := range []string{"zzzzzz", "mmmmmm", "aaaaaa"} {
		id := uploadNamed(t, s, 40, nonce, "file")
		uploaded = append(uploaded, id.String())
	}
	// Newest-by-mtime is uploaded[2], then [1], then [0].
	first, cursor, err := s.List("", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].ID != uploaded[2] {
		t.Fatalf("got %+v, want first entry %s", first, uploaded[2])
	}

	// Delete the file the cursor points at before resuming the page.
	fid, err := ParseID(uploaded[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(fid); err != nil {
		t.Fatal(err)
	}

	rest, next, err := s.List(cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("unexpected nextCursor %q", next)
	}
	if len(rest) != 2 || rest[0].ID != uploaded[1] || rest[1].ID != uploaded[0] {
		t.Fatalf("got %+v, want [%s, %s]", rest, uploaded[1], uploaded[0])
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
