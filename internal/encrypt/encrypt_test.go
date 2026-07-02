package encrypt

import (
	"bytes"
	"errors"
	"io"
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
