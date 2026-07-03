package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// keyHeader mirrors keyHeader in internal/server/api.go — the wire contract
// for encrypted uploads. Duplicated by hand rather than imported: it's a
// small, stable constant and ferryupload otherwise has no dependency on the
// server package.
const keyHeader = "X-Encryption-Key"

type uploadParams struct {
	server        *url.URL
	apiKey        string
	body          io.Reader
	size          int64 // -1 if unknown
	filename      string
	slug          string
	expireDays    int
	hasExpireDays bool
	encryptKey    string
	quiet         bool
}

type uploadResult struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// upload streams body to the server's /api/upload and returns the resulting
// share URL. It only returns once the whole request has been read back to
// completion by draining the response after the response, since the server
// flushes the {id,url} JSON before the upload finishes and severs the
// connection early on failure (see internal/server/api.go:handleUpload).
func upload(p uploadParams) (string, error) {
	q := url.Values{}
	q.Set("filename", p.filename)
	if p.slug != "" {
		q.Set("slug", p.slug)
	}
	if p.hasExpireDays {
		q.Set("expireDays", strconv.Itoa(p.expireDays))
	}
	if p.encryptKey != "" {
		q.Set("encrypt", "true")
	}

	u := *p.server
	u.Path = strings.TrimSuffix(u.Path, "/") + "/api/upload"
	u.RawQuery = q.Encode()

	pr := newProgressReader(p.body, p.size, p.quiet)
	req, err := http.NewRequest(http.MethodPost, u.String(), io.NopCloser(pr))
	if err != nil {
		return "", err
	}
	if p.size >= 0 {
		req.ContentLength = p.size
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	if p.encryptKey != "" {
		req.Header.Set(keyHeader, p.encryptKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	var result uploadResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}
	if result.URL == "" {
		return "", fmt.Errorf("server response missing url")
	}
	// The connection stays open (Connection: close, no further body) until
	// the server commits the upload; draining confirms it actually finished
	// rather than being aborted partway through.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		return "", fmt.Errorf("upload did not complete: %w", err)
	}
	return result.URL, nil
}
