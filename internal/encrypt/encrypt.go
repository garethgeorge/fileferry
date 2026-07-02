// Package encrypt provides streaming AES-256-GCM encryption for uploaded
// files. The caller-supplied key (passed through the request, never stored) is
// stretched into an AES key with PBKDF2 over a per-file random salt, so the
// same passphrase yields a distinct key for every file.
//
// The on-disk format is a 24-byte header (16-byte salt + 8-byte nonce prefix)
// followed by a sequence of length-prefixed chunks:
//
//	salt[16] | noncePrefix[8]
//	( len[4] big-endian | ciphertext[len] )*
//
// The decrypted plaintext itself begins with a small metadata frame — a
// uint16-length-prefixed byte string (the original filename) — followed by the
// file content. Because the metadata rides inside the AEAD it is both hidden
// (the ".encr" URL leaks no content type) and tamper-protected.
//
// Each chunk holds up to chunkSize plaintext bytes sealed under a fresh nonce
// (noncePrefix || 4-byte big-endian counter), so encryption and decryption
// both stream without buffering the whole file. End-of-stream is the reader
// hitting EOF on a chunk boundary; because storage is trusted, that EOF is
// authoritative and no explicit terminator is needed. A wrong key surfaces as
// ErrWrongKey when the first chunk's GCM tag fails to verify.
package encrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrWrongKey is returned when decryption fails to authenticate, which for a
// file written by this package means the supplied key is wrong (or, rarely,
// the stored ciphertext is corrupt).
var ErrWrongKey = errors.New("wrong key")

const (
	saltLen   = 16
	prefixLen = 8 // random nonce prefix; the remaining 4 nonce bytes are a counter
	chunkSize = 64 * 1024
	// pbkdf2Iters runs once per file (not per chunk), so a high count is cheap
	// relative to the transfer while still slowing brute force on weak keys.
	pbkdf2Iters = 200_000
	// MaxMetaLen bounds the metadata frame (uint16-length-prefixed).
	MaxMetaLen = 65535
)

func deriveAEAD(key string, salt []byte) (cipher.AEAD, error) {
	dk, err := pbkdf2.Key(sha256.New, key, salt, pbkdf2Iters, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dk)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// nonceFor builds the 12-byte nonce for a chunk from the file's random prefix
// and the chunk counter.
func nonceFor(prefix []byte, counter uint32) []byte {
	nonce := make([]byte, prefixLen+4)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[prefixLen:], counter)
	return nonce
}

// Writer encrypts plaintext written to it and streams the header and chunks to
// an underlying writer. Callers must call Close to flush the trailing chunk.
type Writer struct {
	dst         io.Writer
	aead        cipher.AEAD
	header      []byte // salt || noncePrefix, emitted lazily before the first chunk
	prefix      []byte // slice into header
	counter     uint32
	buf         []byte // pending plaintext, always < chunkSize after a flush
	wroteHeader bool
	err         error
}

// NewWriter returns a Writer that encrypts under key and writes to dst. meta is
// stored as the first bytes of the (encrypted) plaintext and recovered by the
// Reader; it carries the original filename so nothing about the content leaks
// through the ".encr" URL.
func NewWriter(dst io.Writer, key string, meta []byte) (*Writer, error) {
	if len(meta) > MaxMetaLen {
		return nil, fmt.Errorf("encrypt: metadata too long (%d > %d)", len(meta), MaxMetaLen)
	}
	header := make([]byte, saltLen+prefixLen)
	if _, err := rand.Read(header); err != nil {
		return nil, err
	}
	aead, err := deriveAEAD(key, header[:saltLen])
	if err != nil {
		return nil, err
	}
	// Seed the plaintext buffer with the length-prefixed metadata frame so it
	// is encrypted along with (and ahead of) the content.
	frame := make([]byte, 2, 2+len(meta))
	binary.BigEndian.PutUint16(frame, uint16(len(meta)))
	frame = append(frame, meta...)
	return &Writer{
		dst:    dst,
		aead:   aead,
		header: header,
		prefix: header[saltLen:],
		buf:    frame,
	}, nil
}

func (e *Writer) ensureHeader() error {
	if e.wroteHeader {
		return nil
	}
	if _, err := e.dst.Write(e.header); err != nil {
		return err
	}
	e.wroteHeader = true
	return nil
}

func (e *Writer) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	if err := e.ensureHeader(); err != nil {
		e.err = err
		return 0, err
	}
	e.buf = append(e.buf, p...)
	for len(e.buf) >= chunkSize {
		if err := e.sealChunk(e.buf[:chunkSize]); err != nil {
			e.err = err
			return 0, err
		}
		e.buf = e.buf[chunkSize:]
	}
	return len(p), nil
}

func (e *Writer) sealChunk(plaintext []byte) error {
	nonce := nonceFor(e.prefix, e.counter)
	e.counter++
	ct := e.aead.Seal(nil, nonce, plaintext, nil)
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(ct)))
	if _, err := e.dst.Write(lb[:]); err != nil {
		return err
	}
	_, err := e.dst.Write(ct)
	return err
}

// Close flushes any buffered plaintext as the final chunk.
func (e *Writer) Close() error {
	if e.err != nil {
		return e.err
	}
	if err := e.ensureHeader(); err != nil {
		e.err = err
		return err
	}
	e.err = e.sealChunk(e.buf)
	e.buf = nil
	return e.err
}

// Reader decrypts a stream produced by Writer. It validates the key eagerly in
// NewReader by decrypting the first chunk, so a wrong key is reported before
// any plaintext is served.
type Reader struct {
	src     io.Reader
	aead    cipher.AEAD
	prefix  []byte
	counter uint32
	buf     []byte // decrypted plaintext not yet returned
	meta    []byte // metadata frame recovered from the head of the plaintext
	eof     bool
	err     error
}

// NewReader reads the header from src, derives the key, decrypts the first
// chunk to validate it, and recovers the metadata frame. It returns
// ErrWrongKey if authentication fails.
func NewReader(src io.Reader, key string) (*Reader, error) {
	header := make([]byte, saltLen+prefixLen)
	if _, err := io.ReadFull(src, header); err != nil {
		// Too short to even hold a header: treat as an undecryptable file.
		return nil, ErrWrongKey
	}
	aead, err := deriveAEAD(key, header[:saltLen])
	if err != nil {
		return nil, err
	}
	r := &Reader{src: src, aead: aead, prefix: header[saltLen:]}
	// Eagerly decrypt the first chunk so a bad key fails here, not mid-stream.
	if err := r.fill(); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if err := r.readMeta(); err != nil {
		return nil, err
	}
	return r, nil
}

// Meta returns the metadata (original filename) stored at upload time.
func (r *Reader) Meta() []byte { return r.meta }

// readMeta strips the leading uint16-length-prefixed metadata frame off the
// decrypted plaintext, pulling more chunks if it spans a boundary. It runs only
// after a successful decryption, so the length is authenticated.
func (r *Reader) readMeta() error {
	if err := r.want(2); err != nil {
		return err
	}
	metaLen := int(binary.BigEndian.Uint16(r.buf[:2]))
	if err := r.want(2 + metaLen); err != nil {
		return err
	}
	r.meta = append([]byte(nil), r.buf[2:2+metaLen]...)
	r.buf = r.buf[2+metaLen:]
	return nil
}

// want ensures at least n bytes are buffered, decrypting further chunks as
// needed. A premature EOF means a truncated/corrupt file.
func (r *Reader) want(n int) error {
	for len(r.buf) < n {
		if r.eof {
			return ErrWrongKey
		}
		if err := r.fill(); err != nil {
			if errors.Is(err, io.EOF) {
				return ErrWrongKey
			}
			return err
		}
	}
	return nil
}

// fill decrypts the next chunk and appends its plaintext to r.buf. It returns
// io.EOF at a clean chunk boundary (end of file) and ErrWrongKey on an
// authentication failure.
func (r *Reader) fill() error {
	var lb [4]byte
	if _, err := io.ReadFull(r.src, lb[:]); err != nil {
		if errors.Is(err, io.EOF) {
			r.eof = true
			return io.EOF
		}
		return fmt.Errorf("encrypt: truncated chunk length: %w", err)
	}
	n := binary.BigEndian.Uint32(lb[:])
	if n < uint32(r.aead.Overhead()) || n > chunkSize+uint32(r.aead.Overhead()) {
		return ErrWrongKey
	}
	ct := make([]byte, n)
	if _, err := io.ReadFull(r.src, ct); err != nil {
		return fmt.Errorf("encrypt: truncated chunk body: %w", err)
	}
	nonce := nonceFor(r.prefix, r.counter)
	r.counter++
	pt, err := r.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return ErrWrongKey
	}
	r.buf = append(r.buf, pt...)
	return nil
}

func (r *Reader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	for len(r.buf) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		if err := r.fill(); err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			r.err = err
			return 0, err
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
