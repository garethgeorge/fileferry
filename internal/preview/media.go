package preview

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"strings"
)

//go:embed media.html
var mediaTemplateSource string

var mediaTemplate = template.Must(template.New("media").Parse(mediaTemplateSource))

// NewMedia returns a Previewer that renders an HTML page with a native player
// (<video>/<audio>) or <img> for video, audio, and image files. The media
// element loads the raw bytes as a subresource (via ?raw), so range requests
// and seeking work and the raw response's sandbox CSP does not apply.
func NewMedia() Previewer {
	return &media{}
}

type media struct{}

// Matches reports whether f is a video, audio, or image file.
func (m *media) Matches(f File) bool {
	return mediaKind(f.MimeType) != ""
}

// ServeHTTP writes an HTML page embedding f in the appropriate media element.
func (m *media) ServeHTTP(w http.ResponseWriter, r *http.Request, f File) error {
	// A bare type (no charset) is what <source type=...> expects.
	mimeType, _, _ := strings.Cut(f.MimeType, ";")
	var page bytes.Buffer
	data := struct {
		ID       string
		Kind     string
		MimeType string
	}{
		ID:       f.ID,
		Kind:     mediaKind(f.MimeType),
		MimeType: strings.TrimSpace(mimeType),
	}
	if err := mediaTemplate.Execute(&page, data); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := w.Write(page.Bytes())
	return err
}

// mediaKind classifies a MIME type as "video", "audio", or "image"; "" means
// this previewer does not handle it.
func mediaKind(mimeType string) string {
	switch {
	case strings.HasPrefix(mimeType, "video/"):
		return "video"
	case strings.HasPrefix(mimeType, "audio/"):
		return "audio"
	case strings.HasPrefix(mimeType, "image/"):
		return "image"
	default:
		return ""
	}
}
