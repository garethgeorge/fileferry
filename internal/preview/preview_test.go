package preview

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMimeTypeForExt(t *testing.T) {
	if got := MimeTypeForExt(""); got != "application/octet-stream" {
		t.Errorf("MimeTypeForExt(%q) = %q, want application/octet-stream", "", got)
	}
	if got := MimeTypeForExt("qqq"); got != "application/octet-stream" {
		t.Errorf("MimeTypeForExt(%q) = %q, want application/octet-stream", "qqq", got)
	}
	if got := MimeTypeForExt("go"); !strings.HasPrefix(got, "text/") {
		t.Errorf("MimeTypeForExt(%q) = %q, want text/ prefix", "go", got)
	}
	if got := MimeTypeForExt("png"); got != "image/png" {
		t.Errorf("MimeTypeForExt(%q) = %q, want image/png", "png", got)
	}
	if got := MimeTypeForExt("json"); !strings.Contains(got, "charset") {
		t.Errorf("MimeTypeForExt(%q) = %q, want charset present", "json", got)
	}
}

func TestTextMatches(t *testing.T) {
	p := NewText()

	cases := []struct {
		name string
		f    File
		want bool
	}{
		{"go source", File{Ext: "go", MimeType: "text/plain; charset=utf-8", Size: 1024}, true},
		{"binary", File{Ext: "bin", MimeType: "application/octet-stream", Size: 10}, false},
		{"too large", File{Ext: "txt", MimeType: "text/plain; charset=utf-8", Size: 2 << 20}, false},
	}
	for _, tc := range cases {
		if got := p.Matches(tc.f); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTextServeHTTP(t *testing.T) {
	p := NewText()

	const raw = "<script>alert(1)</script>"
	f := File{
		ID:       "ap-abc123-note.txt",
		Ext:      "txt",
		MimeType: "text/plain; charset=utf-8",
		Size:     int64(len(raw)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(raw))), nil
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/"+f.ID, nil)
	if err := p.ServeHTTP(rec, req, f); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}

	res := rec.Result()
	if res.StatusCode != 200 {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	body := rec.Body.String()
	if strings.Contains(body, raw) {
		t.Errorf("body contains unescaped raw content %q", raw)
	}
	if !strings.Contains(body, "/raw/") {
		t.Errorf("body missing raw link /raw/")
	}
}

func TestMarkdownMatches(t *testing.T) {
	p := NewMarkdown()

	cases := []struct {
		name string
		f    File
		want bool
	}{
		{"md", File{Ext: "md", MimeType: "text/markdown; charset=utf-8", Size: 100}, true},
		{"markdown", File{Ext: "markdown", MimeType: "text/markdown; charset=utf-8", Size: 100}, true},
		{"too large", File{Ext: "md", MimeType: "text/markdown; charset=utf-8", Size: 2 << 20}, false},
		{"plain text", File{Ext: "txt", MimeType: "text/plain; charset=utf-8", Size: 100}, false},
	}
	for _, tc := range cases {
		if got := p.Matches(tc.f); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMarkdownServeHTTP(t *testing.T) {
	p := NewMarkdown()

	const raw = "# Title\n\nSome **bold** text.\n\n<script>alert(1)</script>\n"
	f := File{
		ID:       "ap-abc123-doc.md",
		Ext:      "md",
		MimeType: "text/markdown; charset=utf-8",
		Size:     int64(len(raw)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(raw))), nil
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/"+f.ID, nil)
	if err := p.ServeHTTP(rec, req, f); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}

	res := rec.Result()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<h1") || !strings.Contains(body, "<strong>bold</strong>") {
		t.Errorf("markdown not rendered to HTML:\n%s", body)
	}
	// Raw HTML in the source must not survive into the output (no XSS).
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("raw <script> leaked into rendered output:\n%s", body)
	}
}

func TestArchiveMatches(t *testing.T) {
	p := NewArchive()

	cases := []struct {
		name string
		f    File
		want bool
	}{
		{"zip", File{Ext: "zip", Size: 1000}, true},
		{"tar", File{Ext: "tar", Size: 1000}, true},
		{"tgz", File{Ext: "tgz", Size: 1000}, true},
		{"gz", File{Ext: "gz", Size: 1000}, true},
		{"too large", File{Ext: "zip", Size: maxArchivePreviewSize + 1}, false},
		{"empty", File{Ext: "zip", Size: 0}, false},
		{"other", File{Ext: "png", Size: 1000}, false},
	}
	for _, tc := range cases {
		if got := p.Matches(tc.f); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestArchiveServeZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w1, _ := zw.Create("dir/hello.txt")
	w1.Write([]byte("hello world"))
	zw.Create("dir/") // a directory entry
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	body := serveArchive(t, "ap-abc123.zip", "zip", buf.Bytes())
	if !strings.Contains(body, "zip archive") {
		t.Errorf("missing archive kind:\n%s", body)
	}
	if !strings.Contains(body, "dir/hello.txt") {
		t.Errorf("missing file entry:\n%s", body)
	}
	// Directory entries render with a single trailing slash, not a doubled one.
	if strings.Contains(body, "dir//") {
		t.Errorf("directory entry has a doubled trailing slash:\n%s", body)
	}
	if !strings.Contains(body, ">dir/<") {
		t.Errorf("directory entry missing single trailing slash:\n%s", body)
	}
}

func TestArchiveServeTarGz(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	content := []byte("some file contents")
	tw.WriteHeader(&tar.Header{Name: "notes.md", Mode: 0o644, Size: int64(len(content))})
	tw.Write(content)
	tw.Close()

	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	gz.Write(raw.Bytes())
	gz.Close()

	body := serveArchive(t, "ap-abc123.gz", "gz", gzBuf.Bytes())
	if !strings.Contains(body, "tar.gz archive") {
		t.Errorf("missing archive kind:\n%s", body)
	}
	if !strings.Contains(body, "notes.md") {
		t.Errorf("missing tar entry:\n%s", body)
	}
}

func TestArchiveGzFallbackNotTar(t *testing.T) {
	// A plain gzip stream (not a tar) should be served raw, not as a listing.
	var gzBuf bytes.Buffer
	gz := gzip.NewWriter(&gzBuf)
	gz.Write([]byte("just some gzipped text, not a tarball"))
	gz.Close()

	p := NewArchive()
	f := File{
		ID:       "ap-abc123.gz",
		Ext:      "gz",
		MimeType: "application/gzip",
		Size:     int64(gzBuf.Len()),
		Open:     func() (io.ReadCloser, error) { return seekableCloser{bytes.NewReader(gzBuf.Bytes())}, nil },
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/"+f.ID, nil)
	if err := p.ServeHTTP(rec, req, f); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	res := rec.Result()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/gzip") {
		t.Errorf("fallback Content-Type = %q, want application/gzip", ct)
	}
	if strings.Contains(rec.Body.String(), "<table") {
		t.Errorf("plain gzip should not render an archive listing")
	}
	if got := rec.Body.String(); got != string(gzBuf.Bytes()) {
		t.Errorf("fallback body does not match original content")
	}
}

// TestArchiveNonSeekableSource covers the buffered fallback used when the
// source doesn't support random access (e.g. a decrypting stream): archive.go
// must still list entries correctly without a Seeker/ReaderAt available.
func TestArchiveNonSeekableSource(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w1, _ := zw.Create("hello.txt")
	w1.Write([]byte("hello world"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	p := NewArchive()
	f := File{
		ID:       "ap-abc123.zip",
		Ext:      "zip",
		MimeType: "application/zip",
		Size:     int64(buf.Len()),
		// io.NopCloser erases ReaderAt/Seeker even though bytes.Reader has
		// them, mirroring a genuinely non-seekable stream (e.g. decrypting a
		// completed upload).
		Open: func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(buf.Bytes())), nil },
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/"+f.ID, nil)
	if err := p.ServeHTTP(rec, req, f); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello.txt") {
		t.Errorf("missing file entry:\n%s", rec.Body.String())
	}
}

// seekableCloser adapts a *bytes.Reader (which supports ReaderAt/Seeker, like
// the *os.File a real upload is backed by) into an io.ReadCloser, so tests
// exercise the streaming/random-access code path rather than the buffered
// fallback used for non-seekable sources.
type seekableCloser struct{ *bytes.Reader }

func (seekableCloser) Close() error { return nil }

// serveArchive runs the archive previewer over data and returns the body.
func serveArchive(t *testing.T, id, ext string, data []byte) string {
	t.Helper()
	p := NewArchive()
	f := File{
		ID:       id,
		Ext:      ext,
		MimeType: "application/octet-stream",
		Size:     int64(len(data)),
		Open:     func() (io.ReadCloser, error) { return seekableCloser{bytes.NewReader(data)}, nil },
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/"+id, nil)
	if err := p.ServeHTTP(rec, req, f); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}
	if ct := rec.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	return rec.Body.String()
}

func TestRedirectMatches(t *testing.T) {
	p := NewRedirect()

	cases := []struct {
		name string
		f    File
		want bool
	}{
		{"link ext", File{Ext: "link", Size: 20}, true},
		{"too large", File{Ext: "link", Size: maxRedirectSize + 1}, false},
		{"other ext", File{Ext: "txt", Size: 20}, false},
	}
	for _, tc := range cases {
		if got := p.Matches(tc.f); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRedirectServeHTTPValid(t *testing.T) {
	p := NewRedirect()

	const target = "https://example.com/path?q=1"
	f := File{
		ID:   "ap-abc123.link",
		Ext:  "link",
		Size: int64(len(target)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(target + "\n"))), nil // trailing newline, as a browser paste would have
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/"+f.ID, nil)
	if err := p.ServeHTTP(rec, req, f); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}

	res := rec.Result()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `content="2;url=`+target) {
		t.Errorf("body missing meta-refresh to target:\n%s", body)
	}
	if !strings.Contains(body, `href="`+target+`"`) {
		t.Errorf("body missing link to target:\n%s", body)
	}
}

func TestRedirectServeHTTPInvalid(t *testing.T) {
	p := NewRedirect()

	const raw = "not a url"
	f := File{
		ID:   "ap-abc123.link",
		Ext:  "link",
		Size: int64(len(raw)),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader([]byte(raw))), nil
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/"+f.ID, nil)
	if err := p.ServeHTTP(rec, req, f); err != nil {
		t.Fatalf("ServeHTTP error: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "http-equiv=\"refresh\"") {
		t.Errorf("invalid content should not render a meta-refresh:\n%s", body)
	}
	if !strings.Contains(body, "/raw/") {
		t.Errorf("invalid content should offer a raw-content link:\n%s", body)
	}
}

func TestRegistryFind(t *testing.T) {
	md := NewMarkdown()
	txt := NewText()
	arc := NewArchive()
	reg := NewRegistry(md, txt, arc)

	nonMatch := File{Ext: "bin", MimeType: "application/octet-stream", Size: 10}
	if got := reg.Find(nonMatch); got != nil {
		t.Errorf("Find(non-matching) = %v, want nil", got)
	}

	match := File{Ext: "go", MimeType: "text/plain; charset=utf-8", Size: 1024}
	if got := reg.Find(match); got != txt {
		t.Errorf("Find(matching) = %v, want text previewer", got)
	}

	// Media types have no previewer; they fall through to raw serving.
	video := File{Ext: "mp4", MimeType: "video/mp4"}
	if got := reg.Find(video); got != nil {
		t.Errorf("Find(video) = %v, want nil (served raw)", got)
	}

	zipFile := File{Ext: "zip", MimeType: "application/zip", Size: 1000}
	if got := reg.Find(zipFile); got != arc {
		t.Errorf("Find(zip) = %v, want archive previewer", got)
	}
}
