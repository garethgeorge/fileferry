package store

import (
	"crypto/rand"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

// idAlphabet is lowercase Crockford base32 (no i, l, o, u).
const idAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// idEpoch is the reference instant for week numbering.
var idEpoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

const weekDuration = 7 * 24 * time.Hour

// ErrInvalidID is wrapped by ParseID on any malformed input.
var ErrInvalidID = errors.New("invalid file id")

// FileID identifies a stored file. Its string form is
// "<week>-<nonce>[-<slug>][.<ext>]".
type FileID struct {
	Week  int
	Nonce string
	Slug  string
	Ext   string
}

// NewID mints a fresh FileID for the given time, slug and originating
// filename. The slug is sanitized and the extension is derived from filename.
func NewID(now time.Time, slug, filename string) FileID {
	return FileID{
		Week:  weekOf(now),
		Nonce: newNonce(),
		Slug:  sanitizeSlug(slug),
		Ext:   sanitizeExt(filename),
	}
}

// String renders the FileID as its canonical filename form.
func (id FileID) String() string {
	var b strings.Builder
	b.WriteString(encodeWeek(id.Week))
	b.WriteByte('-')
	b.WriteString(id.Nonce)
	if id.Slug != "" {
		b.WriteByte('-')
		b.WriteString(id.Slug)
	}
	if id.Ext != "" {
		b.WriteByte('.')
		b.WriteString(id.Ext)
	}
	return b.String()
}

// MonthDir returns the "2006-01" directory for the week this ID belongs to.
func (id FileID) MonthDir() string {
	return weekStart(id.Week).Format("2006-01")
}

// UploadedAt returns the start instant of this ID's week.
func (id FileID) UploadedAt() time.Time {
	return weekStart(id.Week)
}

// ParseID parses a filename into a FileID. All failures wrap ErrInvalidID.
func ParseID(s string) (FileID, error) {
	name := s
	ext := ""
	// Split extension at the LAST ".".
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		cand := s[i+1:]
		if !isValidExt(cand) {
			return FileID{}, ErrInvalidID
		}
		ext = cand
		name = s[:i]
	}
	// Slugs never contain "."; any leftover dot in the name is invalid.
	if strings.ContainsRune(name, '.') {
		return FileID{}, ErrInvalidID
	}

	parts := strings.SplitN(name, "-", 3)
	if len(parts) < 2 {
		return FileID{}, ErrInvalidID
	}

	// Week.
	weekStr := parts[0]
	if len(weekStr) < 1 || !allInAlphabet(weekStr) {
		return FileID{}, ErrInvalidID
	}
	week := decodeWeek(weekStr)

	// Nonce: exactly 6 alphabet chars.
	nonce := parts[1]
	if len(nonce) != 6 || !allInAlphabet(nonce) {
		return FileID{}, ErrInvalidID
	}

	// Slug (optional).
	slug := ""
	if len(parts) == 3 {
		slug = parts[2]
		if !isValidSlug(slug) {
			return FileID{}, ErrInvalidID
		}
	}

	return FileID{Week: week, Nonce: nonce, Slug: slug, Ext: ext}, nil
}

// newNonce returns a fresh 6-character nonce drawn from idAlphabet.
func newNonce() string {
	const n = 6
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	out := make([]byte, n)
	for i, b := range buf {
		out[i] = idAlphabet[int(b)%len(idAlphabet)]
	}
	return string(out)
}

// weekOf returns the number of whole weeks between idEpoch and t.
func weekOf(t time.Time) int {
	return int(t.Sub(idEpoch) / weekDuration)
}

// weekStart returns the instant at which the given week begins.
func weekStart(week int) time.Time {
	return idEpoch.Add(time.Duration(week) * weekDuration)
}

// encodeWeek base32-encodes week (MSB first), zero-padded to width >= 2.
func encodeWeek(week int) string {
	if week <= 0 {
		return "00"
	}
	var digits []byte
	for week > 0 {
		digits = append(digits, idAlphabet[week%len(idAlphabet)])
		week /= len(idAlphabet)
	}
	// Reverse in place (digits are least-significant first).
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	if len(digits) < 2 {
		digits = append([]byte{'0'}, digits...)
	}
	return string(digits)
}

// decodeWeek decodes a base32 week string. Caller must ensure allInAlphabet.
func decodeWeek(s string) int {
	week := 0
	for i := 0; i < len(s); i++ {
		week = week*len(idAlphabet) + strings.IndexByte(idAlphabet, s[i])
	}
	return week
}

// sanitizeSlug lowercases input, collapses non-[a-z0-9] runs to single "-",
// trims dashes, and truncates to 64 chars. May return "".
func sanitizeSlug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 64 {
		out = out[:64]
	}
	return strings.Trim(out, "-")
}

// sanitizeExt derives a lowercase extension (no dot) matching [a-z0-9]{1,10},
// else "".
func sanitizeExt(filename string) string {
	ext := filepath.Ext(filename)
	ext = strings.TrimPrefix(ext, ".")
	ext = strings.ToLower(ext)
	if !isValidExt(ext) {
		return ""
	}
	return ext
}

// allInAlphabet reports whether every byte of s is in idAlphabet.
func allInAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(idAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// isValidExt reports whether s matches [a-z0-9]{1,10}.
func isValidExt(s string) bool {
	if len(s) < 1 || len(s) > 10 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// isValidSlug reports whether s matches [a-z0-9-]+ without leading/trailing "-".
func isValidSlug(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}
