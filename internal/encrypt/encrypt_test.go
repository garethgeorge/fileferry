package encrypt

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
)

func roundtrip(t *testing.T, key string, plaintext []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRoundtrip(t *testing.T) {
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 7} {
		plaintext := bytes.Repeat([]byte("fileferry-encryption!"), size/21+1)[:size]
		ct := roundtrip(t, "correct horse", plaintext)
		// Only meaningful once the plaintext is long enough that a coincidental
		// byte match in the random-looking ciphertext is astronomically
		// unlikely; for tiny inputs (e.g. a single byte) a match is expected.
		if size >= 16 && bytes.Contains(ct, plaintext) {
			t.Fatalf("size %d: ciphertext contains plaintext", size)
		}

		r, err := NewReader(bytes.NewReader(ct), "correct horse")
		if err != nil {
			t.Fatalf("size %d: NewReader: %v", size, err)
		}
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("size %d: read: %v", size, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("size %d: got %d bytes, want %d", size, len(got), len(plaintext))
		}
	}
}

func TestWrongKey(t *testing.T) {
	ct := roundtrip(t, "right-key", []byte("secret data"))
	if _, err := NewReader(bytes.NewReader(ct), "wrong-key"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("NewReader with wrong key: got %v, want ErrWrongKey", err)
	}
}

func TestWrongKeyEmptyFile(t *testing.T) {
	ct := roundtrip(t, "right-key", nil)
	if _, err := NewReader(bytes.NewReader(ct), "wrong-key"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("empty file wrong key: got %v, want ErrWrongKey", err)
	}
	// Right key on the empty file yields empty plaintext, not an error.
	r, err := NewReader(bytes.NewReader(ct), "right-key")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty roundtrip: got %q err %v", got, err)
	}
}

func TestTruncatedIsWrongKey(t *testing.T) {
	ct := roundtrip(t, "k", []byte("some content here"))
	// Chop the header so the file can't be decrypted at all.
	if _, err := NewReader(bytes.NewReader(ct[:4]), "k"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("short file: got %v, want ErrWrongKey", err)
	}
}

func TestMetadataRoundtrip(t *testing.T) {
	var buf bytes.Buffer
	meta := []byte("my secret résumé.pdf")
	w, err := NewWriter(&buf, "k", meta)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("x"), 3*chunkSize) // force meta + several chunks
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The plaintext metadata must not appear in the ciphertext.
	if bytes.Contains(buf.Bytes(), meta) {
		t.Fatal("metadata leaked into ciphertext")
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()), "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(r.Meta(), meta) {
		t.Fatalf("meta = %q, want %q", r.Meta(), meta)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content = %d bytes, want %d", len(got), len(content))
	}
}

// Distinct salts mean the same key/plaintext encrypt to different ciphertext.
func TestSaltRandomizesCiphertext(t *testing.T) {
	a := roundtrip(t, "k", []byte("dup"))
	b := roundtrip(t, "k", []byte("dup"))
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of the same input produced identical ciphertext")
	}
}

func TestRandomAccessRoundtrip(t *testing.T) {
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 7} {
		plaintext := bytes.Repeat([]byte("fileferry-encryption!"), size/21+1)[:size]
		ct := roundtrip(t, "correct horse", plaintext)

		ra, err := NewRandomAccessReader(bytes.NewReader(ct), int64(len(ct)), "correct horse")
		if err != nil {
			t.Fatalf("size %d: NewRandomAccessReader: %v", size, err)
		}
		if got := ra.Size(); got != int64(size) {
			t.Fatalf("size %d: Size() = %d, want %d", size, got, size)
		}

		// Whole-file read via io.SectionReader, exactly as download.go composes it.
		got, err := io.ReadAll(io.NewSectionReader(ra, 0, ra.Size()))
		if err != nil {
			t.Fatalf("size %d: read all: %v", size, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("size %d: got %d bytes, want %d", size, len(got), len(plaintext))
		}
	}
}

// TestRandomAccessMatchesSequential decrypts the same multi-chunk ciphertext
// through both readers and checks they agree, including on partial,
// chunk-boundary-crossing ranges (the part of ReadAt most likely to have an
// off-by-one).
func TestRandomAccessMatchesSequential(t *testing.T) {
	plaintext := bytes.Repeat([]byte("0123456789"), (3*chunkSize+123)/10+1)
	ct := roundtrip(t, "k", plaintext)

	seq, err := NewReader(bytes.NewReader(ct), "k")
	if err != nil {
		t.Fatal(err)
	}
	wantAll, err := io.ReadAll(seq)
	if err != nil {
		t.Fatal(err)
	}

	ra, err := NewRandomAccessReader(bytes.NewReader(ct), int64(len(ct)), "k")
	if err != nil {
		t.Fatal(err)
	}
	if int(ra.Size()) != len(wantAll) {
		t.Fatalf("Size() = %d, want %d", ra.Size(), len(wantAll))
	}

	ranges := []struct{ off, n int }{
		{0, 10},                          // start of chunk 0
		{chunkSize - 5, 10},              // straddles chunk 0/1
		{chunkSize, 10},                  // start of chunk 1
		{2*chunkSize - 3, 6},             // straddles chunk 1/2
		{3 * chunkSize, 50},              // start of the final, partial chunk
		{len(wantAll) - 1, 1},            // last byte
		{0, len(wantAll)},                // everything
	}
	for _, rg := range ranges {
		buf := make([]byte, rg.n)
		n, err := ra.ReadAt(buf, int64(rg.off))
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("ReadAt(off=%d,n=%d): %v", rg.off, rg.n, err)
		}
		want := wantAll[rg.off : rg.off+n]
		if !bytes.Equal(buf[:n], want) {
			t.Fatalf("ReadAt(off=%d,n=%d) = %q, want %q", rg.off, rg.n, buf[:n], want)
		}
	}
}

func TestRandomAccessOutOfRange(t *testing.T) {
	ct := roundtrip(t, "k", []byte("short content"))
	ra, err := NewRandomAccessReader(bytes.NewReader(ct), int64(len(ct)), "k")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	n, err := ra.ReadAt(buf, ra.Size())
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("ReadAt at Size(): n=%d err=%v, want 0,io.EOF", n, err)
	}
	if _, err := ra.ReadAt(buf, -1); err == nil {
		t.Fatal("ReadAt with negative offset: got nil error")
	}
}

func TestRandomAccessWrongKey(t *testing.T) {
	ct := roundtrip(t, "right-key", []byte("secret data"))
	if _, err := NewRandomAccessReader(bytes.NewReader(ct), int64(len(ct)), "wrong-key"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("NewRandomAccessReader with wrong key: got %v, want ErrWrongKey", err)
	}
}

// TestRandomAccessTruncated chops bytes off the end (destroying the sentinel
// and trailer, and shifting the last chunk's boundary), which must be
// detected rather than silently producing a wrong-size or garbage read.
func TestRandomAccessTruncated(t *testing.T) {
	ct := roundtrip(t, "k", bytes.Repeat([]byte("x"), 3*chunkSize))
	short := ct[:len(ct)-100]
	if _, err := NewRandomAccessReader(bytes.NewReader(short), int64(len(short)), "k"); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("truncated file: got %v, want ErrWrongKey", err)
	}
}

// TestRandomAccessCorruptedChunk flips a byte inside the second chunk's
// ciphertext. Construction only touches chunk 0 (for the metadata frame), so
// it must succeed; a ReadAt into the corrupted region must fail rather than
// return unauthenticated plaintext.
func TestRandomAccessCorruptedChunk(t *testing.T) {
	plaintext := bytes.Repeat([]byte("y"), 2*chunkSize)
	ct := roundtrip(t, "k", plaintext)
	corrupt := append([]byte(nil), ct...)
	const gcmOverhead = 16 // standard AES-GCM tag size
	chunk1Off := headerLen + (lenPrefixLen + chunkSize + gcmOverhead)
	corrupt[chunk1Off+lenPrefixLen] ^= 0xFF // first ciphertext byte of chunk 1

	ra, err := NewRandomAccessReader(bytes.NewReader(corrupt), int64(len(corrupt)), "k")
	if err != nil {
		t.Fatalf("construction should succeed (only chunk 0 is touched): %v", err)
	}
	buf := make([]byte, 10)
	if _, err := ra.ReadAt(buf, chunkSize); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("ReadAt into corrupted chunk: got %v, want ErrWrongKey", err)
	}
	// The untouched first chunk must still read correctly.
	if _, err := ra.ReadAt(buf, 0); err != nil {
		t.Fatalf("ReadAt into untouched chunk 0: %v", err)
	}
	if !bytes.Equal(buf, plaintext[:10]) {
		t.Fatalf("chunk 0 content = %q, want %q", buf, plaintext[:10])
	}
}

// TestRandomAccessConcurrent exercises the io.ReaderAt contract's requirement
// that concurrent ReadAt calls on the same source are safe (run with -race).
func TestRandomAccessConcurrent(t *testing.T) {
	plaintext := bytes.Repeat([]byte("concurrent-access-"), 5000) // spans several chunks
	ct := roundtrip(t, "k", plaintext)
	ra, err := NewRandomAccessReader(bytes.NewReader(ct), int64(len(ct)), "k")
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for g := range 32 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			off := (g * 977) % len(plaintext) // scattered, overlapping offsets
			n := 200
			if off+n > len(plaintext) {
				n = len(plaintext) - off
			}
			buf := make([]byte, n)
			if _, err := ra.ReadAt(buf, int64(off)); err != nil {
				errs <- err
				return
			}
			if !bytes.Equal(buf, plaintext[off:off+n]) {
				errs <- fmt.Errorf("goroutine %d: mismatch at offset %d", g, off)
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestRandomAccessMetadata(t *testing.T) {
	var buf bytes.Buffer
	meta := []byte("my secret résumé.pdf")
	w, err := NewWriter(&buf, "k", meta)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("x"), 3*chunkSize)
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	ra, err := NewRandomAccessReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "k")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ra.Meta(), meta) {
		t.Fatalf("meta = %q, want %q", ra.Meta(), meta)
	}
	if int(ra.Size()) != len(content) {
		t.Fatalf("Size() = %d, want %d", ra.Size(), len(content))
	}
	got, err := io.ReadAll(io.NewSectionReader(ra, 0, ra.Size()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("content = %d bytes, want %d", len(got), len(content))
	}
}
