package preview

import (
	"bytes"
	_ "embed"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// redirectExt is the extension the client uses to mark a file as a short-link
// redirect (see the "URL suffix" field in the web UI): it uploads the pasted
// link with filename "link.link", and this previewer takes it from there.
const redirectExt = "link"

// maxRedirectSize caps both matching and reading: a ".link" file holds nothing
// but a single link, so anything larger is treated as an ordinary (non-link)
// file and falls through to the next previewer.
const maxRedirectSize = 64 * 1024

//go:embed redirect.html
var redirectTemplateSource string

var redirectTemplate = template.Must(template.New("redirect").Parse(redirectTemplateSource))

// NewRedirect returns a Previewer for ".link" files: instead of showing the
// link as plain text, it serves a brief "Redirecting…" page that sends the
// browser on to the target after a couple of seconds.
func NewRedirect() Previewer {
	return &redirect{}
}

type redirect struct{}

func (p *redirect) Matches(f File) bool {
	if f.Size > maxRedirectSize {
		return false
	}
	return f.Ext == redirectExt
}

// ServeHTTP reads f's content and, if it is a single absolute http(s) URL,
// renders the redirect page. A ".link" file whose content isn't a URL falls
// back to a page that just shows the raw content, since treating arbitrary
// content as a redirect target would be unsafe.
func (p *redirect) ServeHTTP(w http.ResponseWriter, r *http.Request, f File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	content, err := io.ReadAll(io.LimitReader(rc, maxRedirectSize))
	if err != nil {
		return err
	}
	target, ok := redirectTarget(content)

	var page bytes.Buffer
	data := struct {
		ID     string
		Target string
		Valid  bool
	}{ID: f.ID, Target: target, Valid: ok}
	if err := redirectTemplate.Execute(&page, data); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err = w.Write(page.Bytes())
	return err
}

// redirectTarget reports whether content, once trimmed, is a single absolute
// http(s) URL with no embedded whitespace.
func redirectTarget(content []byte) (string, bool) {
	s := strings.TrimSpace(string(content))
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return "", false
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return s, true
}
