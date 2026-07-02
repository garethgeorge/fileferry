package preview

import (
	"bytes"
	_ "embed"
	"html/template"
	"io"
	"net/http"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// maxMarkdownPreviewSize caps both matching and reading. Larger files fall
// through to the text previewer (raw, syntax-highlighted).
const maxMarkdownPreviewSize = 1 << 20 // 1 MiB

//go:embed markdown.html
var markdownTemplateSource string

var markdownTemplate = template.Must(template.New("markdown").Parse(markdownTemplateSource))

// NewMarkdown returns a Previewer that renders Markdown files to HTML. Raw
// HTML embedded in the Markdown is omitted and dangerous links are dropped
// (goldmark's safe default — WithUnsafe is deliberately not set), so the
// rendered output cannot inject script; a script-src 'none' CSP is added as
// defense in depth.
func NewMarkdown() Previewer {
	return &markdown{
		md: goldmark.New(
			goldmark.WithExtensions(extension.GFM),
		),
	}
}

type markdown struct {
	md goldmark.Markdown
}

// Matches reports whether f is a Markdown file small enough to render.
func (m *markdown) Matches(f File) bool {
	if f.Size > maxMarkdownPreviewSize {
		return false
	}
	return f.Ext == "md" || f.Ext == "markdown"
}

// ServeHTTP reads f's content, renders it to HTML, and writes the preview.
func (m *markdown) ServeHTTP(w http.ResponseWriter, r *http.Request, f File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	src, err := io.ReadAll(io.LimitReader(rc, maxMarkdownPreviewSize))
	if err != nil {
		return err
	}

	var body bytes.Buffer
	if err := m.md.Convert(src, &body); err != nil {
		return err
	}

	var page bytes.Buffer
	data := struct {
		ID   string
		Body template.HTML
	}{
		ID:   f.ID,
		Body: template.HTML(body.String()), // goldmark output is sanitized (no raw HTML)
	}
	if err := markdownTemplate.Execute(&page, data); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "script-src 'none'")
	_, err = w.Write(page.Bytes())
	return err
}
