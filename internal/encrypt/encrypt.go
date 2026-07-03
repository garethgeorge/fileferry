// Package encrypt provides streaming AES-256-GCM encryption for uploaded
// files, with random-access decryption for completed files. The
// caller-supplied key (passed through the request, never stored) is
// stretched into an AES key with PBKDF2 over a per-file random salt, so the
// same passphrase yields a distinct key for every file.
//
// The on-disk format is a 24-byte header (16-byte salt + 8-byte nonce
// prefix), a sequence of length-prefixed chunks, a 4-byte zero sentinel, and
// an 8-byte trailer:
//
//	salt[16] | noncePrefix[8]
//	( len[4] | ciphertext[len] )*
//	end[4]=0 | plaintextSize[8, big-endian]
//
// Each chunk holds up to chunkSize plaintext bytes sealed under a fresh nonce
// (noncePrefix || 4-byte big-endian counter). Every chunk before the last is
// exactly chunkSize bytes, so a chunk's ciphertext offset is a direct
// function of its index alone — no scanning required. Chunks are also
// authenticated independently (nothing chains one to the next), so any chunk
// can be decrypted without first processing the ones before it.
//
// This lets Reader decrypt sequentially without buffering (encryption and
// decryption both stream), while RandomAccessReader — given the exact stored
// file size, e.g. from a stat — seeks straight to any chunk a read touches
// and decrypts only that chunk. That's what lets the archive previewer list
// a zip's central directory, and lets completed encrypted downloads serve
// HTTP range requests, without ever buffering the whole file into memory.
//
// A real chunk's ciphertext is always at least aead.Overhead() bytes (the
// GCM tag alone, for an empty final chunk), so a length prefix of 0 can never
// belong to a real chunk — it unambiguously marks the sentinel preceding the
// trailer. A consequence: a stream that ends before the sentinel (e.g. cut
// short by a failed upload) is now detected as truncated rather than being
// silently accepted at whatever point it happens to stop.
//
// The decrypted plaintext itself begins with a small metadata frame — a
// uint16-length-prefixed byte string (the original filename) — followed by
// the file content. Because the metadata rides inside the AEAD it is both
// hidden (the ".encr" URL leaks no content type) and tamper-protected.
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
// the stored ciphertext is corrupt or truncated).
var ErrWrongKey = errors.New("wrong key")

const (
	saltLen      = 16
	prefixLen    = 8 // random nonce prefix; the remaining 4 nonce bytes are a counter
	headerLen    = saltLen + prefixLen
	chunkSize    = 64 * 1024
	lenPrefixLen = 4 // per-chunk ciphertext length prefix; also the width of the 0 sentinel
	trailerLen   = 8 // plaintext stream size, big-endian, following the sentinel
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
// an underlying writer. Callers must call Close to flush the trailing chunk
// and write the terminator/trailer.
type Writer struct {
	dst         io.Writer
	aead        cipher.AEAD
	header      []byte // salt || noncePrefix, emitted lazily before the first chunk
	prefix      []byte // slice into header
	counter     uint32
	buf         []byte // pending plaintext, always < chunkSize after a flush
	wroteHeader bool
	total       int64 // running plaintext stream size (metadata frame + content)
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
		total:  int64(len(frame)),
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
	e.total += int64(len(p))
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
	var lb [lenPrefixLen]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(ct)))
	if _, err := e.dst.Write(lb[:]); err != nil {
		return err
	}
	_, err := e.dst.Write(ct)
	return err
}

// Close flushes any buffered plaintext as the final chunk, then writes the
// 0-length end-of-chunks sentinel and the total plaintext size. The trailer
// lets a RandomAccessReader locate every chunk (including the last) and
// validate the file's length without scanning; the sentinel lets a
// sequential Reader tell a real chunk from the trailer that follows it.
func (e *Writer) Close() error {
	if e.err != nil {
		return e.err
	}
	if err := e.ensureHeader(); err != nil {
		e.err = err
		return err
	}
	if err := e.sealChunk(e.buf); err != nil {
		e.err = err
		return err
	}
	e.buf = nil
	var tail [lenPrefixLen + trailerLen]byte
	binary.BigEndian.PutUint64(tail[lenPrefixLen:], uint64(e.total))
	if _, err := e.dst.Write(tail[:]); err != nil {
		e.err = err
		return err
	}
	return nil
}

// Reader decrypts a stream produced by Writer, sequentially from the start.
// It validates the key eagerly in NewReader by decrypting the first chunk,
// so a wrong key is reported before any plaintext is served. Use this for a
// source that can only be read once, forward — such as a still-uploading
// file; a completed file should use RandomAccessReader instead, which also
// supports Read/Seek via io.SectionReader.
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

// fill decrypts the next chunk and appends its plaintext to r.buf. A length
// prefix of 0 is the end-of-stream sentinel written by Writer.Close: a real
// chunk's ciphertext is never empty, since even a 0-byte final chunk still
// carries the GCM tag. Reaching the underlying reader's EOF before that
// sentinel means the stream was cut short.
func (r *Reader) fill() error {
	var lb [lenPrefixLen]byte
	if _, err := io.ReadFull(r.src, lb[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return ErrWrongKey
		}
		return fmt.Errorf("encrypt: truncated chunk length: %w", err)
	}
	n := binary.BigEndian.Uint32(lb[:])
	if n == 0 {
		r.eof = true
		return io.EOF
	}
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

// RandomAccessReader decrypts a completed ciphertext (see Writer) with
// random access: ReadAt decrypts only the chunk(s) a given read touches, so
// listing a zip's central directory or serving an HTTP range request never
// requires reading the whole file. src must expose the exact stored
// ciphertext (e.g. an *os.File), and srcSize must be its exact length (e.g.
// from os.Stat) — both are needed up front to locate the trailer and
// validate the chunk layout.
type RandomAccessReader struct {
	src    io.ReaderAt
	aead   cipher.AEAD
	prefix []byte

	total  int64 // plaintext stream size (metadata frame + content), from the trailer
	chunks int64 // number of chunks, always >= 1
	last   int64 // plaintext length of the final chunk (may be 0)

	meta    []byte
	metaLen int64 // len(frame) = 2+len(meta); shifts content offsets within the stream
}

// NewRandomAccessReader opens a completed ciphertext for random-access reads.
// It reads the header and trailer, validates the resulting chunk layout
// against srcSize, and decrypts enough of the start to authenticate the key
// and recover the metadata frame — so a wrong key is reported immediately,
// the same as NewReader.
func NewRandomAccessReader(src io.ReaderAt, srcSize int64, key string) (*RandomAccessReader, error) {
	if srcSize < trailerLen {
		return nil, ErrWrongKey
	}
	header := make([]byte, headerLen)
	if _, err := src.ReadAt(header, 0); err != nil {
		return nil, ErrWrongKey
	}
	aead, err := deriveAEAD(key, header[:saltLen])
	if err != nil {
		return nil, err
	}
	var trailer [trailerLen]byte
	if _, err := src.ReadAt(trailer[:], srcSize-trailerLen); err != nil {
		return nil, ErrWrongKey
	}

	r := &RandomAccessReader{
		src:    src,
		aead:   aead,
		prefix: header[saltLen:],
		total:  int64(binary.BigEndian.Uint64(trailer[:])),
	}
	if r.total < 0 {
		return nil, ErrWrongKey
	}
	r.chunks = r.total/chunkSize + 1
	r.last = r.total % chunkSize

	// The trailer isn't authenticated on its own, so cross-check it against
	// the file's actual size before trusting it for chunk math: any
	// tampering or corruption almost certainly breaks this equality.
	lastOff := r.chunkCiphertextOffset(r.chunks - 1)
	lastCiphertextLen := int64(lenPrefixLen) + r.last + int64(aead.Overhead())
	wantSize := lastOff + lastCiphertextLen + lenPrefixLen /* sentinel */ + trailerLen
	if wantSize != srcSize {
		return nil, ErrWrongKey
	}

	head, err := r.readStreamPrefix(2)
	if err != nil {
		return nil, err
	}
	metaLen := int64(binary.BigEndian.Uint16(head))
	frameLen := 2 + metaLen
	frame, err := r.readStreamPrefix(frameLen)
	if err != nil {
		return nil, err
	}
	r.meta = append([]byte(nil), frame[2:]...)
	r.metaLen = frameLen
	return r, nil
}

// Meta returns the metadata (original filename) stored at upload time.
func (r *RandomAccessReader) Meta() []byte { return r.meta }

// Size returns the exact decrypted content length, excluding the metadata
// frame: the length a previewer or range request should treat as the file's
// size.
func (r *RandomAccessReader) Size() int64 { return r.total - r.metaLen }

// ReadAt implements io.ReaderAt over the decrypted content (excluding the
// metadata frame). Safe for concurrent use.
func (r *RandomAccessReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("encrypt: negative offset")
	}
	streamOff := off + r.metaLen
	n := 0
	for n < len(p) {
		cur := streamOff + int64(n)
		if cur >= r.total {
			return n, io.EOF
		}
		pt, err := r.chunk(cur / chunkSize)
		if err != nil {
			return n, err
		}
		n += copy(p[n:], pt[cur%chunkSize:])
	}
	return n, nil
}

// readStreamPrefix reads the first n plaintext-stream bytes, spanning chunks
// as needed. It's used only during construction, to recover the metadata
// frame before metaLen is known — so it addresses the stream directly,
// unlike the content-relative ReadAt.
func (r *RandomAccessReader) readStreamPrefix(n int64) ([]byte, error) {
	out := make([]byte, 0, n)
	for int64(len(out)) < n {
		pt, err := r.chunk(int64(len(out)) / chunkSize)
		if err != nil {
			return nil, err
		}
		start := int64(len(out)) % chunkSize
		if start >= int64(len(pt)) {
			return nil, ErrWrongKey
		}
		out = append(out, pt[start:]...)
	}
	return out[:n], nil
}

// chunkCiphertextOffset returns the on-disk offset of chunk i's length
// prefix. Every chunk before the last is exactly chunkSize plaintext bytes,
// so this is a direct function of i — no scanning required.
func (r *RandomAccessReader) chunkCiphertextOffset(i int64) int64 {
	return headerLen + i*(int64(lenPrefixLen)+chunkSize+int64(r.aead.Overhead()))
}

func (r *RandomAccessReader) chunkPlaintextLen(i int64) int64 {
	if i == r.chunks-1 {
		return r.last
	}
	return chunkSize
}

// chunk decrypts and returns chunk i's plaintext. It's stateless — no cache,
// no lock — decrypting a 64KiB GCM chunk is cheap, so re-decrypting on an
// occasional overlapping ReadAt costs little next to the simplicity of
// having no shared mutable state to guard. Chunk i's nonce is directly
// computable from its index, so chunks authenticate independently:
// decrypting chunk i never requires processing any other chunk first, and
// concurrent calls (required by the io.ReaderAt contract) need no
// synchronization.
func (r *RandomAccessReader) chunk(i int64) ([]byte, error) {
	if i < 0 || i >= r.chunks {
		return nil, fmt.Errorf("encrypt: chunk %d out of range", i)
	}
	off := r.chunkCiphertextOffset(i)
	buf := make([]byte, int64(lenPrefixLen)+r.chunkPlaintextLen(i)+int64(r.aead.Overhead()))
	if _, err := r.src.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("encrypt: reading chunk %d: %w", i, err)
	}
	gotLen := binary.BigEndian.Uint32(buf[:lenPrefixLen])
	ct := buf[lenPrefixLen:]
	if int64(gotLen) != int64(len(ct)) {
		return nil, ErrWrongKey
	}
	pt, err := r.aead.Open(nil, nonceFor(r.prefix, uint32(i)), ct, nil)
	if err != nil {
		return nil, ErrWrongKey
	}
	return pt, nil
}
