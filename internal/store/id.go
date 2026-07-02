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

// idEpoch is the reference instant for day numbering. It predates any real
// upload so day numbers are already 3 base32 digits wide, keeping the encoded
// width consistent (2 digits = 1024 days ≈ 2002; 3 digits carry through ~2089).
var idEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

const dayDuration = 24 * time.Hour

// ErrInvalidID is wrapped by ParseID on any malformed input.
var ErrInvalidID = errors.New("invalid file id")

// FileID identifies a stored file. Its string form is
// "<day>-<nonce>[-<slug>][.<ext>]".
type FileID struct {
	Day   int
	Nonce string
	Slug  string
	Ext   string
}

// NewID mints a fresh FileID for the given time, URL suffix and originating
// filename. The extension is normally derived from filename, but for textual
// content the suffix may carry a custom extension ("<slug>.<ext>") that
// overrides it — so a pasted snippet can be relabeled ".md", ".json", etc.
// while the slug keeps the remainder.
func NewID(now time.Time, suffix, filename string, contentIsText bool) FileID {
	slug := sanitizeSlug(suffix)
	ext := sanitizeExt(filename)
	if contentIsText {
		if base, custom := splitSuffixExt(suffix); custom != "" {
			slug, ext = base, custom
		}
	}
	return FileID{
		Day:   dayOf(now),
		Nonce: newNonce(),
		Slug:  slug,
		Ext:   ext,
	}
}

// splitSuffixExt splits a user-supplied suffix into a sanitized slug and a
// custom extension taken from a trailing ".<ext>". If the trailing token is
// not a valid extension there is no custom extension and the whole suffix is
// the slug.
func splitSuffixExt(suffix string) (slug, ext string) {
	if i := strings.LastIndexByte(suffix, '.'); i >= 0 {
		if cand := strings.ToLower(suffix[i+1:]); isValidExt(cand) {
			return sanitizeSlug(suffix[:i]), cand
		}
	}
	return sanitizeSlug(suffix), ""
}

// String renders the FileID as its canonical filename form.
func (id FileID) String() string {
	var b strings.Builder
	b.WriteString(encodeDay(id.Day))
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

// MonthDir returns the "2006-01" directory for the day this ID belongs to. A
// day never spans two calendar months, so this always matches the upload date's
// month.
func (id FileID) MonthDir() string {
	return dayStart(id.Day).Format("2006-01")
}

// UploadedAt returns the start instant (UTC midnight) of this ID's day.
func (id FileID) UploadedAt() time.Time {
	return dayStart(id.Day)
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

	// Day.
	dayStr := parts[0]
	if len(dayStr) < 1 || !allInAlphabet(dayStr) {
		return FileID{}, ErrInvalidID
	}
	day := decodeDay(dayStr)

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

	return FileID{Day: day, Nonce: nonce, Slug: slug, Ext: ext}, nil
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

// dayOf returns the number of whole days between idEpoch and t.
func dayOf(t time.Time) int {
	return int(t.Sub(idEpoch) / dayDuration)
}

// dayStart returns the instant (UTC midnight) at which the given day begins.
func dayStart(day int) time.Time {
	return idEpoch.Add(time.Duration(day) * dayDuration)
}

// encodeDay base32-encodes day (MSB first), zero-padded to width >= 3.
func encodeDay(day int) string {
	if day <= 0 {
		return "000"
	}
	var digits []byte
	for day > 0 {
		digits = append(digits, idAlphabet[day%len(idAlphabet)])
		day /= len(idAlphabet)
	}
	// Reverse in place (digits are least-significant first).
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}
	for len(digits) < 3 {
		digits = append([]byte{'0'}, digits...)
	}
	return string(digits)
}

// decodeDay decodes a base32 day string. Caller must ensure allInAlphabet.
func decodeDay(s string) int {
	day := 0
	for i := 0; i < len(s); i++ {
		day = day*len(idAlphabet) + strings.IndexByte(idAlphabet, s[i])
	}
	return day
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
