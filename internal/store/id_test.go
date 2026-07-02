package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEncodeWeek(t *testing.T) {
	cases := []struct {
		week int
		want string
	}{
		{0, "00"},
		{31, "0z"},
		{32, "10"},
		{1023, "zz"},
		{1024, "100"},
	}
	for _, c := range cases {
		if got := encodeWeek(c.week); got != c.want {
			t.Errorf("encodeWeek(%d) = %q, want %q", c.week, got, c.want)
		}
		// Round-trip through decodeWeek.
		if got := decodeWeek(c.want); got != c.week {
			t.Errorf("decodeWeek(%q) = %d, want %d", c.want, got, c.week)
		}
	}
}

func TestWeekOfWeekStart(t *testing.T) {
	epoch := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

	if got := weekOf(epoch); got != 0 {
		t.Errorf("weekOf(epoch) = %d, want 0", got)
	}
	if got := weekOf(epoch.Add(6*24*time.Hour + 23*time.Hour)); got != 0 {
		t.Errorf("weekOf(epoch+6d23h) = %d, want 0", got)
	}
	if got := weekOf(epoch.Add(7 * 24 * time.Hour)); got != 1 {
		t.Errorf("weekOf(epoch+7d) = %d, want 1", got)
	}

	// weekStart(weekOf(t)) is <= t and within 7 days.
	samples := []time.Time{
		epoch,
		epoch.Add(3*24*time.Hour + 5*time.Hour),
		epoch.Add(400 * 24 * time.Hour),
		time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC),
	}
	for _, ts := range samples {
		start := weekStart(weekOf(ts))
		if start.After(ts) {
			t.Errorf("weekStart(weekOf(%v)) = %v is after t", ts, start)
		}
		if ts.Sub(start) >= 7*24*time.Hour {
			t.Errorf("weekStart(weekOf(%v)) = %v not within 7 days", ts, start)
		}
	}
}

func TestMonthDirUsesWeekStart(t *testing.T) {
	// Week 4 starts 2020-01-29 and spills into February; the dir must be the
	// START month, 2020-01.
	id := FileID{Week: 4, Nonce: "p9m2rr"}
	start := weekStart(4)
	if start.Format("2006-01-02") != "2020-01-29" {
		t.Fatalf("precondition: weekStart(4) = %v, want 2020-01-29", start)
	}
	if got := id.MonthDir(); got != "2020-01" {
		t.Errorf("MonthDir() = %q, want %q", got, "2020-01")
	}
	if !id.UploadedAt().Equal(start) {
		t.Errorf("UploadedAt() = %v, want %v", id.UploadedAt(), start)
	}
}

func TestStringParseRoundTrip(t *testing.T) {
	cases := []FileID{
		{Week: 339, Nonce: "p9m2rr", Slug: "my-notes", Ext: "txt"},
		{Week: 339, Nonce: "x7f3q2", Ext: "png"},
		{Week: 339, Nonce: "x7f3q2"},
		{Week: 0, Nonce: "000000", Slug: "hello"},
		{Week: 1024, Nonce: "zzzzzz", Slug: "a", Ext: "gz"},
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
	id := NewID(now, "My Notes!", "notes.txt")
	if id.Slug != "my-notes" {
		t.Errorf("NewID slug = %q, want %q", id.Slug, "my-notes")
	}
	if id.Ext != "txt" {
		t.Errorf("NewID ext = %q, want %q", id.Ext, "txt")
	}
	if len(id.Nonce) != 6 {
		t.Errorf("NewID nonce = %q, want length 6", id.Nonce)
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
	other := NewID(now, "My Notes!", "notes.txt")
	if other.Nonce == id.Nonce {
		t.Errorf("two NewID calls produced same nonce %q", id.Nonce)
	}
}
