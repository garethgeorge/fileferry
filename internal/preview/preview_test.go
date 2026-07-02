package preview

import (
	"bytes"
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
	if !strings.Contains(body, "?raw=1") {
		t.Errorf("body missing raw link ?raw=1")
	}
}

func TestMediaMatches(t *testing.T) {
	p := NewMedia()

	cases := []struct {
		name string
		f    File
		want bool
	}{
		{"video", File{Ext: "mp4", MimeType: "video/mp4"}, true},
		{"audio", File{Ext: "mp3", MimeType: "audio/mpeg"}, true},
		{"image", File{Ext: "png", MimeType: "image/png"}, true},
		{"text", File{Ext: "txt", MimeType: "text/plain; charset=utf-8"}, false},
		{"binary", File{Ext: "bin", MimeType: "application/octet-stream"}, false},
	}
	for _, tc := range cases {
		if got := p.Matches(tc.f); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMediaServeHTTP(t *testing.T) {
	p := NewMedia()
	f := File{ID: "ap-abc123-clip.mp4", Ext: "mp4", MimeType: "video/mp4"}

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
	if !strings.Contains(body, "<video") {
		t.Errorf("body missing <video> element:\n%s", body)
	}
	if !strings.Contains(body, "/"+f.ID+"?raw=1") {
		t.Errorf("body missing raw media source")
	}
	if !strings.Contains(body, `type="video/mp4"`) {
		t.Errorf("body missing bare mime type on <source>")
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

func TestRegistryFind(t *testing.T) {
	md := NewMarkdown()
	txt := NewText()
	med := NewMedia()
	reg := NewRegistry(md, txt, med)

	nonMatch := File{Ext: "bin", MimeType: "application/octet-stream", Size: 10}
	if got := reg.Find(nonMatch); got != nil {
		t.Errorf("Find(non-matching) = %v, want nil", got)
	}

	match := File{Ext: "go", MimeType: "text/plain; charset=utf-8", Size: 1024}
	if got := reg.Find(match); got != txt {
		t.Errorf("Find(matching) = %v, want text previewer", got)
	}

	video := File{Ext: "mp4", MimeType: "video/mp4"}
	if got := reg.Find(video); got != med {
		t.Errorf("Find(video) = %v, want media previewer", got)
	}
}
