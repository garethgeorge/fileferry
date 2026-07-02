package preview

import (
	"bytes"
	_ "embed"
	"html/template"
	"io"
	"net/http"
	"strings"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// maxTextPreviewSize caps both matching and reading. Files larger than this are
// served raw rather than highlighted.
const maxTextPreviewSize = 1 << 20 // 1 MiB

//go:embed text.html
var textTemplateSource string

var textTemplate = template.Must(template.New("text").Parse(textTemplateSource))

// NewText returns a Previewer that renders text and source files with
// chroma-based syntax highlighting.
func NewText() Previewer {
	return &text{
		formatter: html.New(html.WithClasses(false)),
	}
}

type text struct {
	formatter *html.Formatter
}

// Matches reports whether f is a text-like file small enough to highlight.
func (t *text) Matches(f File) bool {
	if f.Size > maxTextPreviewSize {
		return false
	}
	if strings.HasPrefix(f.MimeType, "text/") || strings.HasPrefix(f.MimeType, "application/json") {
		return true
	}
	if f.Ext != "" && lexers.Match("x."+f.Ext) != nil {
		return true
	}
	return false
}

// ServeHTTP reads f's content, highlights it, and writes an HTML preview.
func (t *text) ServeHTTP(w http.ResponseWriter, r *http.Request, f File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	content, err := io.ReadAll(io.LimitReader(rc, maxTextPreviewSize))
	if err != nil {
		return err
	}

	// Pick a lexer: by filename first, then by content analysis, then fallback.
	lexer := lexers.Match("x." + f.Ext)
	if lexer == nil {
		lexer = lexers.Analyse(string(content))
	}
	if lexer == nil {
		lexer = lexers.Fallback
	}

	style := styles.Get("github")
	if style == nil {
		style = styles.Fallback
	}

	iterator, err := lexer.Tokenise(nil, string(content))
	if err != nil {
		return err
	}

	var code bytes.Buffer
	if err := t.formatter.Format(&code, style, iterator); err != nil {
		return err
	}

	var page bytes.Buffer
	data := struct {
		ID   string
		Code template.HTML
	}{
		ID:   f.ID,
		Code: template.HTML(code.String()), // chroma escapes file content itself
	}
	if err := textTemplate.Execute(&page, data); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = w.Write(page.Bytes())
	return err
}
