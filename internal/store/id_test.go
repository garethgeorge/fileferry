package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEncodeDay(t *testing.T) {
	cases := []struct {
		day  int
		want string
	}{
		{0, "000"},
		{31, "00z"},
		{32, "010"},
		{1023, "0zz"},
		{1024, "100"},
		{32767, "zzz"},
		{32768, "1000"}, // rolls to 4 digits ≈ year 2089
	}
	for _, c := range cases {
		if got := encodeDay(c.day); got != c.want {
			t.Errorf("encodeDay(%d) = %q, want %q", c.day, got, c.want)
		}
		// Round-trip through decodeDay.
		if got := decodeDay(c.want); got != c.day {
			t.Errorf("decodeDay(%q) = %d, want %d", c.want, got, c.day)
		}
	}
}

func TestDayOfDayStart(t *testing.T) {
	epoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

	if got := dayOf(epoch); got != 0 {
		t.Errorf("dayOf(epoch) = %d, want 0", got)
	}
	if got := dayOf(epoch.Add(23 * time.Hour)); got != 0 {
		t.Errorf("dayOf(epoch+23h) = %d, want 0", got)
	}
	if got := dayOf(epoch.Add(24 * time.Hour)); got != 1 {
		t.Errorf("dayOf(epoch+24h) = %d, want 1", got)
	}

	// dayStart(dayOf(t)) is <= t and within 24 hours.
	samples := []time.Time{
		epoch,
		epoch.Add(5 * time.Hour),
		epoch.Add(4000 * 24 * time.Hour),
		time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
	}
	for _, ts := range samples {
		start := dayStart(dayOf(ts))
		if start.After(ts) {
			t.Errorf("dayStart(dayOf(%v)) = %v is after t", ts, start)
		}
		if ts.Sub(start) >= 24*time.Hour {
			t.Errorf("dayStart(dayOf(%v)) = %v not within 24h", ts, start)
		}
	}
}

func TestMonthDir(t *testing.T) {
	// A day never spans two months, so the dir is always the upload date's
	// month — even on the last day of a month (here the 2024 leap day).
	leapDay := time.Date(2024, 2, 29, 18, 0, 0, 0, time.UTC)
	id := FileID{Day: dayOf(leapDay), Nonce: "p9m2rr"}
	if got := id.MonthDir(); got != "2024-02" {
		t.Errorf("MonthDir() = %q, want %q", got, "2024-02")
	}
	if got := id.UploadedAt(); got.Format("2006-01-02") != "2024-02-29" {
		t.Errorf("UploadedAt() = %v, want 2024-02-29", got)
	}
}

func TestStringParseRoundTrip(t *testing.T) {
	cases := []FileID{
		{Day: 9679, Nonce: "p9m2rr", Slug: "my-notes", Ext: "txt"},
		{Day: 9679, Nonce: "x7f3q2", Ext: "png"},
		{Day: 9679, Nonce: "x7f3q2"},
		{Day: 0, Nonce: "000000", Slug: "hello"},
		{Day: 32768, Nonce: "zzzzzz", Slug: "a", Ext: "gz"},
	}
	for _, want := range cases {
		s := want.String()
		got, err := ParseID(s)
		if err != nil {
			t.Errorf("ParseID(%q) error: %v", s, err)
			continue
		}
		if got != want {
			t.Errorf("round-trip %q: got %+v, want %+v", s, got, want)
		}
	}
}

func TestParseIDReject(t *testing.T) {
	bad := []string{
		"",
		"..",
		"a/b",
		"ab",
		"ab-shrt",                    // 5-char nonce
		"ab-p9m2rrr",                 // 7-char nonce
		"ab-p9m2rr-",                 // trailing dash slug
		"-ab-p9m2rr",                 // leading dash (empty week part)
		"ab-p9m2rr-.txt",             // empty slug before ext
		"AB-P9M2RR.TXT",              // uppercase
		"ab-p9m2rr-slug.",            // trailing dot
		"ab-p9m2rr.tar.gz",           // leftover dot in name
		"ab-p9m2ri",                  // nonce contains 'i'
		"ab-p9m2rl",                  // nonce contains 'l'
		"ab-p9m2ro",                  // nonce contains 'o'
		"ab-p9m2ru",                  // nonce contains 'u'
		"ab-p9m2rr.extensiontoolong", // ext 11+ chars
	}
	for _, s := range bad {
		if _, err := ParseID(s); err == nil {
			t.Errorf("ParseID(%q) = nil error, want ErrInvalidID", s)
		} else if !errors.Is(err, ErrInvalidID) {
			t.Errorf("ParseID(%q) error %v does not wrap ErrInvalidID", s, err)
		}
	}
}

func TestSanitizeSlug(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"My Notes!", "my-notes"},
		{"  --hello--  ", "hello"},
		{"héllo wörld", "h-llo-w-rld"},
		{"!!!", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := sanitizeSlug(c.in); got != c.want {
			t.Errorf("sanitizeSlug(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Long input truncated <= 64 with no trailing "-".
	long := strings.Repeat("a", 60) + "----------" + strings.Repeat("b", 20)
	got := sanitizeSlug(long)
	if len(got) > 64 {
		t.Errorf("sanitizeSlug long len = %d, want <= 64", len(got))
	}
	if strings.HasSuffix(got, "-") {
		t.Errorf("sanitizeSlug long = %q has trailing dash", got)
	}
}

func TestSanitizeExt(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"notes.txt", "txt"},
		{"archive.tar.gz", "gz"},
		{"noext", ""},
		{"bad.T@r", ""},
		{".gitignore", "gitignore"},
		{"x.extensiontoolong", ""},
	}
	for _, c := range cases {
		if got := sanitizeExt(c.in); got != c.want {
			t.Errorf("sanitizeExt(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewID(t *testing.T) {
	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	id := NewID(now, "My Notes!", "notes.txt", false)
	if id.Slug != "my-notes" {
		t.Errorf("NewID slug = %q, want %q", id.Slug, "my-notes")
	}
	if id.Ext != "txt" {
		t.Errorf("NewID ext = %q, want %q", id.Ext, "txt")
	}
	if len(id.Nonce) != 6 {
		t.Errorf("NewID nonce = %q, want length 6", id.Nonce)
	}

	// The day segment is zero-padded to a consistent width of >= 3 chars.
	if daySeg, _, _ := strings.Cut(id.String(), "-"); len(daySeg) < 3 {
		t.Errorf("day segment %q shorter than 3 chars", daySeg)
	}

	// Parseable round-trip.
	got, err := ParseID(id.String())
	if err != nil {
		t.Fatalf("ParseID(NewID) error: %v", err)
	}
	if got != id {
		t.Errorf("NewID round-trip: got %+v, want %+v", got, id)
	}

	// Two calls give different nonces.
	other := NewID(now, "My Notes!", "notes.txt", false)
	if other.Nonce == id.Nonce {
		t.Errorf("two NewID calls produced same nonce %q", id.Nonce)
	}
}

func TestNewIDCustomExtension(t *testing.T) {
	now := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)

	// Text content: a custom extension in the suffix overrides the file's own.
	id := NewID(now, "my-notes.md", "paste.txt", true)
	if id.Slug != "my-notes" || id.Ext != "md" {
		t.Errorf("text override: got slug=%q ext=%q, want my-notes/md", id.Slug, id.Ext)
	}

	// Text content, no custom extension: keep the file's own.
	id = NewID(now, "my-notes", "paste.txt", true)
	if id.Slug != "my-notes" || id.Ext != "txt" {
		t.Errorf("text no-override: got slug=%q ext=%q, want my-notes/txt", id.Slug, id.Ext)
	}

	// Non-text content: the extension is not overridden and the dot is treated
	// as an ordinary slug separator, preserving prior behavior.
	id = NewID(now, "holiday.png", "photo.jpg", false)
	if id.Slug != "holiday-png" || id.Ext != "jpg" {
		t.Errorf("non-text: got slug=%q ext=%q, want holiday-png/jpg", id.Slug, id.Ext)
	}

	// Trailing token that is not a valid extension stays part of the slug.
	id = NewID(now, "report.finalversionlong", "paste.txt", true)
	if id.Slug != "report-finalversionlong" || id.Ext != "txt" {
		t.Errorf("invalid ext: got slug=%q ext=%q, want report-finalversionlong/txt", id.Slug, id.Ext)
	}
}
