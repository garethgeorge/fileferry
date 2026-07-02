package store

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testID(nonce string) FileID {
	return FileID{Day: 339, Nonce: nonce, Slug: "test-file", Ext: "txt"}
}

func TestUploadCommitAndRead(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("aaaaaa"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("hello, fileferry!")
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}

	res, err := s.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if res.File == nil {
		t.Fatal("expected a completed file, got a tail reader")
	}
	got, _ := io.ReadAll(res.File)
	if !bytes.Equal(got, content) {
		t.Fatalf("got %q, want %q", got, content)
	}
	if _, err := os.Stat(s.tempPath(id)); !os.IsNotExist(err) {
		t.Fatal(".tmp still exists after commit")
	}
}

// Three readers tail a file while the writer streams it in chunks; all must
// see the complete content and a clean EOF.
func TestTailFollowConcurrent(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("bbbbbb"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}

	var want bytes.Buffer
	for i := 0; i < 100; i++ {
		want.WriteString(strings.Repeat("chunk", 100))
	}

	var wg sync.WaitGroup
	results := make([][]byte, 3)
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := s.Open(context.Background(), id)
			if err != nil {
				errs[i] = err
				return
			}
			defer res.Close()
			if res.Tail == nil {
				errs[i] = errors.New("expected tail reader")
				return
			}
			results[i], errs[i] = io.ReadAll(res.Tail)
		}(i)
	}

	for i := 0; i < 100; i++ {
		if _, err := w.Write([]byte(strings.Repeat("chunk", 100))); err != nil {
			t.Fatal(err)
		}
		if i%10 == 0 {
			time.Sleep(time.Millisecond) // let readers interleave
		}
	}
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	for i := 0; i < 3; i++ {
		if errs[i] != nil {
			t.Fatalf("reader %d: %v", i, errs[i])
		}
		if !bytes.Equal(results[i], want.Bytes()) {
			t.Fatalf("reader %d: got %d bytes, want %d", i, len(results[i]), want.Len())
		}
	}
}

func TestAbortPropagatesToReaders(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("cccccc"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}

	res, err := s.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()

	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(res.Tail)
		readErr <- err
	}()

	cause := errors.New("network blew up")
	w.Abort(cause)

	select {
	case err := <-readErr:
		if !errors.Is(err, cause) {
			t.Fatalf("reader got %v, want %v", err, cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not terminate after abort")
	}
	if _, err := os.Stat(s.tempPath(id)); !os.IsNotExist(err) {
		t.Fatal(".tmp still exists after abort")
	}
	if _, err := s.Open(context.Background(), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open after abort: got %v, want ErrNotFound", err)
	}
}

func TestReaderCancelledByContext(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("dddddd"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort(nil)

	ctx, cancel := context.WithCancel(context.Background())
	res, err := s.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()

	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(res.Tail)
		readErr <- err
	}()
	cancel()

	select {
	case err := <-readErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not terminate on context cancel")
	}
}

func TestAttachWriterConflictAndNotFound(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("eeeeee"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Abort(nil)
	if _, err := s.AttachWriter(id); !errors.Is(err, ErrConflict) {
		t.Fatalf("second attach: got %v, want ErrConflict", err)
	}
	if _, err := s.AttachWriter(testID("zzzzzz")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown attach: got %v, want ErrNotFound", err)
	}
}

func TestClaimTimeoutAbortsUnclaimedUpload(t *testing.T) {
	old := claimTimeout
	claimTimeout = 20 * time.Millisecond
	defer func() { claimTimeout = old }()

	s := newTestStore(t)
	id, err := s.BeginUpload(testID("ffffff"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for {
		if _, err := s.Open(context.Background(), id); errors.Is(err, ErrNotFound) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("unclaimed upload was not garbage collected")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if _, err := s.AttachWriter(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attach after timeout: got %v, want ErrNotFound", err)
	}
}

func TestNonceRegeneratedOnCollision(t *testing.T) {
	s := newTestStore(t)
	want := testID("gggggg")
	id1, err := s.BeginUpload(want, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.BeginUpload(want, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if id1.Nonce != want.Nonce {
		t.Fatalf("first upload changed nonce: %s", id1.Nonce)
	}
	if id2.Nonce == want.Nonce {
		t.Fatal("second upload did not regenerate the colliding nonce")
	}
}

func TestRemoveActiveUploadTerminatesReaders(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("hhhhhh"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := s.AttachWriter(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	res, err := s.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	readErr := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(res.Tail)
		readErr <- err
	}()

	if err := s.Remove(id); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("reader saw clean EOF after delete, want error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not terminate after delete")
	}
	// The writer's upload is dead: writes fail and Commit reports the abort.
	if _, err := w.Write([]byte("more")); err == nil {
		t.Fatal("write after delete succeeded")
	}
	if err := w.Commit(); err == nil {
		t.Fatal("commit after delete succeeded")
	}
}

func TestRemoveCompletedAndMissing(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("jjjjjj"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := s.AttachWriter(id)
	w.Write([]byte("x"))
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(id); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove missing: got %v, want ErrNotFound", err)
	}
}

func TestLateReaderGetsFullFile(t *testing.T) {
	s := newTestStore(t)
	id, err := s.BeginUpload(testID("kkkkkk"), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	w, _ := s.AttachWriter(id)
	w.Write([]byte("all the content"))
	if err := w.Commit(); err != nil {
		t.Fatal(err)
	}
	res, err := s.Open(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	if res.File == nil {
		t.Fatal("late reader should get the completed file")
	}
}

func TestStartupWipesInProgressUploads(t *testing.T) {
	dir := t.TempDir()
	month := filepath.Join(dir, "2026-06")
	inprogress := filepath.Join(dir, "inprogress")
	if err := os.MkdirAll(month, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inprogress, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(inprogress, "ap-aaaaaa.bin")
	keep := filepath.Join(month, "ap-bbbbbb.bin")
	os.WriteFile(orphan, []byte("partial"), 0o644)
	os.WriteFile(keep, []byte("complete"), 0o644)

	if _, err := New(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("interrupted upload survived startup")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("completed file was removed on startup")
	}
	// The inprogress dir must still exist and be ready for new uploads.
	if info, err := os.Stat(inprogress); err != nil || !info.IsDir() {
		t.Fatal("inprogress dir missing after startup")
	}
}
