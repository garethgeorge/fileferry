// Package preview renders browser previews for certain uploaded file types.
//
// A Registry holds a set of Previewers. Given a File, the server asks the
// Registry to Find a matching Previewer; if one is found it renders an HTML
// preview, otherwise the caller serves the file raw.
package preview

import (
	"io"
	"mime"
	"net/http"
	"strings"
)

// File describes a completed uploaded file.
type File struct {
	ID       string // full file id, e.g. "ap-p9m2rr-my-notes.txt" (charset [a-z0-9-.] only)
	Ext      string // extension without dot, may be ""
	MimeType string
	Size     int64
	Open     func() (io.ReadCloser, error) // opens the content
}

// Previewer renders an HTML preview for files it recognizes.
type Previewer interface {
	// Matches reports whether this previewer wants to render f (may consider MimeType, Ext, Size).
	Matches(f File) bool
	ServeHTTP(w http.ResponseWriter, r *http.Request, f File) error
}

// Registry holds an ordered list of previewers.
type Registry struct {
	previewers []Previewer
}

// NewRegistry returns a Registry that consults previewers in the given order.
func NewRegistry(previewers ...Previewer) *Registry {
	return &Registry{previewers: previewers}
}

// Find returns the first previewer matching f, or nil (nil → caller serves the file raw).
func (reg *Registry) Find(f File) Previewer {
	for _, p := range reg.previewers {
		if p.Matches(f) {
			return p
		}
	}
	return nil
}

// mimeOverrides maps extensions (no dot) to content types, checked before
// mime.TypeByExtension so results are deterministic across operating systems.
var mimeOverrides = map[string]string{
	// plain text / source that should syntax-highlight (must be "text/" prefixed)
	"txt":   "text/plain",
	"log":   "text/plain",
	"ini":   "text/plain",
	"cfg":   "text/plain",
	"conf":  "text/plain",
	"go":    "text/plain",
	"py":    "text/plain",
	"rb":    "text/plain",
	"rs":    "text/plain",
	"c":     "text/plain",
	"h":     "text/plain",
	"cpp":   "text/plain",
	"hpp":   "text/plain",
	"java":  "text/plain",
	"kt":    "text/plain",
	"ts":    "text/plain",
	"tsx":   "text/plain",
	"jsx":   "text/plain",
	"sh":    "text/plain",
	"bash":  "text/plain",
	"zsh":   "text/plain",
	"yaml":  "text/plain",
	"yml":   "text/plain",
	"toml":  "text/plain",
	"sql":   "text/plain",
	"proto": "text/plain",
	"md":    "text/markdown",

	// structured / web
	"json": "application/json",
	"js":   "text/javascript",
	"css":  "text/css",
	"html": "text/html",
	"htm":  "text/html",
	"svg":  "image/svg+xml",

	// images
	"png":  "image/png",
	"apng": "image/apng",
	"jpg":  "image/jpeg",
	"jpeg": "image/jpeg",
	"jfif": "image/jpeg",
	"gif":  "image/gif",
	"webp": "image/webp",
	"avif": "image/avif",
	"bmp":  "image/bmp",
	"ico":  "image/x-icon",

	// video
	"mp4":  "video/mp4",
	"m4v":  "video/mp4",
	"mov":  "video/quicktime",
	"webm": "video/webm",
	"ogv":  "video/ogg",

	// audio
	"mp3":  "audio/mpeg",
	"m4a":  "audio/mp4",
	"aac":  "audio/aac",
	"ogg":  "audio/ogg",
	"oga":  "audio/ogg",
	"opus": "audio/ogg",
	"weba": "audio/webm",
	"wav":  "audio/wav",
	"flac": "audio/flac",

	// documents
	"pdf": "application/pdf",
}

// MimeTypeForExt returns the content type for an extension (no dot):
// a built-in override table first (deterministic across OSes), then
// mime.TypeByExtension; "" or unknown → "application/octet-stream".
func MimeTypeForExt(ext string) string {
	if ext == "" {
		return "application/octet-stream"
	}
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))

	if ct, ok := mimeOverrides[ext]; ok {
		return withCharset(ct)
	}
	if ct := mime.TypeByExtension("." + ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// withCharset appends "; charset=utf-8" to text/* and application/json types.
func withCharset(ct string) string {
	if strings.HasPrefix(ct, "text/") || ct == "application/json" {
		return ct + "; charset=utf-8"
	}
	return ct
}
