package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/garethgeorge/fileferry/internal/preview"
	"github.com/garethgeorge/fileferry/internal/server"
	"github.com/garethgeorge/fileferry/internal/store"
)

const (
	testKey  = "test-key"
	testKey2 = "second-key"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	h := server.New(st, preview.NewRegistry(preview.NewText()), server.Options{
		MaxSize:           1 << 30,
		DefaultExpireDays: 365,
		APIKeys:           []string{testKey, testKey2},
		WebUIKey:          testKey,
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, dataDir
}

type createResponse struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// apiPost issues an authenticated JSON POST to the given API path.
func apiPost(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func createUpload(t *testing.T, ts *httptest.Server, filename, slug string, expireDays int) createResponse {
	t.Helper()
	body := fmt.Sprintf(`{"filename":%q,"slug":%q,"expireDays":%d}`, filename, slug, expireDays)
	resp := apiPost(t, ts, "/api/create", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	var cr createResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	return cr
}

func putBytes(t *testing.T, ts *httptest.Server, id string, content []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/put/"+id, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func createEncryptedUpload(t *testing.T, ts *httptest.Server, filename string) createResponse {
	t.Helper()
	body := fmt.Sprintf(`{"filename":%q,"encrypt":true}`, filename)
	resp := apiPost(t, ts, "/api/create", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	var cr createResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cr.ID, ".encr") {
		t.Fatalf("encrypted id %q does not end in .encr", cr.ID)
	}
	return cr
}

func putEncrypted(t *testing.T, ts *httptest.Server, id, key, filename string, content []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/put/"+id, bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("X-Encryption-Key", key)
	req.Header.Set("X-Filename", filename)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// getWithKey fetches a file supplying the decryption key via the header.
func getWithKey(t *testing.T, url, key string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("X-Encryption-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestEncryptedRoundtrip(t *testing.T) {
	ts, dataDir := newTestServer(t)
	const key = "correct horse battery staple"
	content := bytes.Repeat([]byte("top secret payload\n"), 5000) // spans chunks
	cr := createEncryptedUpload(t, ts, "diagram.png")
	if resp := putEncrypted(t, ts, cr.ID, key, "diagram.png", content); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("encrypted put: status %d", resp.StatusCode)
	}

	// The bytes on disk must be ciphertext, never the plaintext.
	stored := readStored(t, dataDir, cr.ID)
	if bytes.Contains(stored, []byte("top secret payload")) {
		t.Fatal("plaintext found in stored file")
	}
	if bytes.Contains(stored, []byte("diagram.png")) {
		t.Fatal("original filename leaked into stored file")
	}

	// Correct key decrypts, restores the content type from the hidden filename.
	resp := getWithKey(t, ts.URL+"/"+cr.ID, key)
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decrypt get: status %d", resp.StatusCode)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("decrypted %d bytes, want %d", len(got), len(content))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("Content-Type = %q, want image/png (recovered from hidden filename)", ct)
	}
}

func TestEncryptedWrongKeyRefused(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createEncryptedUpload(t, ts, "notes.txt")
	putEncrypted(t, ts, cr.ID, "right-key", "notes.txt", []byte("secret"))

	resp := getWithKey(t, ts.URL+"/"+cr.ID, "wrong-key")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong key: status %d, want 403", resp.StatusCode)
	}
	if !strings.Contains(string(body), "Wrong key") {
		t.Fatalf("wrong key body = %q, want \"Wrong key\"", body)
	}
}

// A browser hit (no key header) gets the decryptor shell, not the ciphertext.
func TestEncryptedNoKeyServesShell(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createEncryptedUpload(t, ts, "notes.txt")
	putEncrypted(t, ts, cr.ID, "k", "notes.txt", []byte("secret"))

	resp, err := http.Get(ts.URL + "/" + cr.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("no-key get: status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("no-key Content-Type = %q, want text/html shell", ct)
	}
	if bytes.Contains(body, []byte("secret")) {
		t.Fatal("shell response leaked ciphertext/plaintext")
	}
	if !bytes.Contains(body, []byte("ffkey")) {
		t.Fatal("shell page does not look like the decryptor bootstrap")
	}
}

// With the key in a cookie (as the bootstrap sets it), a browser navigation
// renders the normal preview pipeline rather than a bespoke viewer.
func TestEncryptedCookieRendersPreview(t *testing.T) {
	ts, _ := newTestServer(t)
	const key = "cookie-key"
	cr := createEncryptedUpload(t, ts, "notes.txt")
	putEncrypted(t, ts, cr.ID, key, "notes.txt", []byte("hello preview"))

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+cr.ID, nil)
	req.AddCookie(&http.Cookie{Name: "ffkey", Value: key})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cookie get: status %d", resp.StatusCode)
	}
	// The text previewer renders an HTML page containing the decrypted content.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("cookie get Content-Type = %q, want text/html preview", ct)
	}
	if !bytes.Contains(page, []byte("hello preview")) {
		t.Fatal("preview page does not contain the decrypted content")
	}
}

func TestEncryptedPutRequiresKey(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createEncryptedUpload(t, ts, "notes.txt")
	// PUT without the key header must be rejected.
	if resp := putBytes(t, ts, cr.ID, []byte("data")); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("keyless encrypted put: status %d, want 400", resp.StatusCode)
	}
}

// readStored returns the raw on-disk bytes for a completed upload.
func readStored(t *testing.T, dataDir, id string) []byte {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dataDir, "*", id))
	if err != nil || len(matches) != 1 {
		t.Fatalf("locate stored file %s: matches=%v err=%v", id, matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUploadDownloadRoundtrip(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createUpload(t, ts, "notes.txt", "", 365)
	if !strings.HasSuffix(cr.URL, "/"+cr.ID) {
		t.Fatalf("url %q does not end in id %q", cr.URL, cr.ID)
	}
	if resp := putBytes(t, ts, cr.ID, []byte("hello world")); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("put: status %d", resp.StatusCode)
	}

	// Raw download.
	resp, err := http.Get(ts.URL + "/" + cr.ID + "?raw=1")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(raw) != "hello world" {
		t.Fatalf("raw body = %q", raw)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("nosniff header missing, got %q", got)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("CSP header = %q, want sandbox", got)
	}

	// Preview (default for text).
	resp, err = http.Get(ts.URL + "/" + cr.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("preview content type = %q", ct)
	}
	if !bytes.Contains(page, []byte("hello")) {
		t.Fatal("preview page does not contain the file content")
	}

	// Forced download disposition.
	resp, err = http.Get(ts.URL + "/" + cr.ID + "?dl=1")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
}

// A downloader that connects mid-upload must receive the whole file.
func TestTailFollowOverHTTP(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createUpload(t, ts, "big.bin", "", 365)

	chunk := bytes.Repeat([]byte("0123456789abcdef"), 1024)
	const chunks = 8

	pr, pw := io.Pipe()
	putDone := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPut, ts.URL+"/api/put/"+cr.ID, pr)
		if err != nil {
			putDone <- err
			return
		}
		req.Header.Set("Authorization", "Bearer "+testKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			putDone <- err
			return
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			putDone <- fmt.Errorf("put status %d", resp.StatusCode)
			return
		}
		putDone <- nil
	}()

	// Publish the first chunk, then attach a follower before the rest.
	if _, err := pw.Write(chunk); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	getBody := make(chan []byte, 1)
	getErr := make(chan error, 1)
	go func() {
		resp, err := http.Get(ts.URL + "/" + cr.ID)
		if err != nil {
			getErr <- err
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			getErr <- err
			return
		}
		getBody <- body
	}()

	for i := 1; i < chunks; i++ {
		if _, err := pw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	pw.Close()
	if err := <-putDone; err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-getErr:
		t.Fatal(err)
	case body := <-getBody:
		if len(body) != len(chunk)*chunks {
			t.Fatalf("follower got %d bytes, want %d", len(body), len(chunk)*chunks)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("follower never finished")
	}
}

// When the uploader dies, followers must see a broken transfer, not clean EOF.
func TestAbortTerminatesFollower(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createUpload(t, ts, "doomed.bin", "", 365)

	pr, pw := io.Pipe()
	go func() {
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/put/"+cr.ID, pr)
		req.Header.Set("Authorization", "Bearer "+testKey)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	if _, err := pw.Write([]byte("some partial data")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)

	resp, err := http.Get(ts.URL + "/" + cr.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	pw.CloseWithError(fmt.Errorf("uploader crashed"))

	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("follower read completed cleanly after uploader failure")
	}
}

func TestConcurrentPutConflicts(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createUpload(t, ts, "a.txt", "", 365)

	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/put/"+cr.ID, pr)
		req.Header.Set("Authorization", "Bearer "+testKey)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	pw.Write([]byte("x")) // ensure the first PUT has attached
	time.Sleep(50 * time.Millisecond)

	if resp := putBytes(t, ts, cr.ID, []byte("interloper")); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second put: status %d, want 409", resp.StatusCode)
	}
}

func TestNotFoundAndInvalidIDs(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, path := range []string{"/zz-zzzzzz.txt", "/notanid", "/UPPER-CASE.txt"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s: status %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestDeleteEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	cr := createUpload(t, ts, "gone.txt", "delete me", 365)
	putBytes(t, ts, cr.ID, []byte("bye"))

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/file/"+cr.ID, nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}

	getResp, err := http.Get(ts.URL + "/" + cr.ID)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: status %d", getResp.StatusCode)
	}

	resp2, err := http.DefaultClient.Do(req.Clone(req.Context()))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("double delete: status %d, want 404", resp2.StatusCode)
	}
}

func TestRawSandboxByType(t *testing.T) {
	ts, _ := newTestServer(t)

	cases := []struct {
		filename    string
		wantSandbox bool
	}{
		{"notes.txt", true},   // inert but kept sandboxed (nosniff is the guard)
		{"page.html", true},   // active document: must stay sandboxed
		{"drawing.svg", true}, // SVG can script: must stay sandboxed
		{"doc.pdf", false},    // native viewer needs no sandbox
		{"photo.png", false},  // inert image
		{"clip.mp4", false},   // inert media
	}
	for _, tc := range cases {
		cr := createUpload(t, ts, tc.filename, "", 365)
		putBytes(t, ts, cr.ID, []byte("data"))

		resp, err := http.Get(ts.URL + "/" + cr.ID + "?raw=1")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: nosniff missing, got %q", tc.filename, got)
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if tc.wantSandbox && csp != "sandbox" {
			t.Errorf("%s: CSP = %q, want sandbox", tc.filename, csp)
		}
		if !tc.wantSandbox && csp != "" {
			t.Errorf("%s: CSP = %q, want none", tc.filename, csp)
		}
	}
}

func TestListEndpoint(t *testing.T) {
	ts, _ := newTestServer(t)
	named := createUpload(t, ts, "a.txt", "my notes", 365)
	putBytes(t, ts, named.ID, []byte("aaa"))
	unnamed := createUpload(t, ts, "b.txt", "", 365)
	putBytes(t, ts, unnamed.ID, []byte("bbb"))

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/list?limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Entries []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Size int64  `json:"size"`
		} `json:"entries"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 2 {
		t.Fatalf("got %d entries, want 2 (named and unnamed)", len(out.Entries))
	}
	// Nonces are random, so locate each entry by ID rather than assuming order.
	byID := map[string]struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Size int64  `json:"size"`
	}{}
	for _, e := range out.Entries {
		byID[e.ID] = e
	}
	if e := byID[named.ID]; e.Slug != "my-notes" || e.Size != 3 {
		t.Fatalf("bad named entry: %+v", e)
	}
	if e, ok := byID[unnamed.ID]; !ok || e.Slug != "" || e.Size != 3 {
		t.Fatalf("bad unnamed entry: %+v (present=%v)", e, ok)
	}
}

func TestCreateWritesExpirationEntry(t *testing.T) {
	ts, dataDir := newTestServer(t)
	cr := createUpload(t, ts, "exp.txt", "", 1)
	putBytes(t, ts, cr.ID, []byte("x"))

	name := filepath.Join(dataDir, "expirations", time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02"))
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("expiration index not written: %v", err)
	}
	if !strings.Contains(string(data), cr.ID) {
		t.Fatalf("expiration index %q does not contain %s", data, cr.ID)
	}
}

func TestNeverExpireWritesNoEntry(t *testing.T) {
	ts, dataDir := newTestServer(t)
	cr := createUpload(t, ts, "keep.txt", "", 0)
	putBytes(t, ts, cr.ID, []byte("x"))

	entries, err := os.ReadDir(filepath.Join(dataDir, "expirations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expireDays=0 wrote an expiration entry: %v", entries[0].Name())
	}
}

func TestIndexAndRedirects(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/upload/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Contains(bytes.ToLower(body), []byte("<html")) {
		t.Fatalf("index: status %d", resp.StatusCode)
	}
	resp, err = http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	// The client follows the redirect to /upload/.
	if resp.Request.URL.Path != "/upload/" {
		t.Fatalf("/ landed on %s, want /upload/", resp.Request.URL.Path)
	}
}

// The /api surface rejects requests without a valid Bearer key and accepts any
// configured key.
func TestAPIRequiresBearerKey(t *testing.T) {
	ts, _ := newTestServer(t)

	// No Authorization header at all.
	resp, err := http.Post(ts.URL+"/api/create", "application/json", strings.NewReader(`{"filename":"x.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("keyless create: status %d, want 401", resp.StatusCode)
	}

	// A bogus key is rejected.
	for _, key := range []string{"wrong-key", ""} {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/list", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bogus key %q: status %d, want 401", key, resp.StatusCode)
		}
	}

	// A second configured key (not the web-UI key) is accepted.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/list", nil)
	req.Header.Set("Authorization", "Bearer "+testKey2)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second configured key: status %d, want 200", resp.StatusCode)
	}
}

// The admin UI is served the ephemeral web key via /upload/config.js, and that
// key authenticates against /api.
func TestConfigJSInjectsWebUIKey(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/upload/config.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Fatalf("config.js Content-Type = %q", ct)
	}
	if !strings.Contains(string(body), `window.FF_API_KEY = "`+testKey+`"`) {
		t.Fatalf("config.js does not inject the web UI key: %q", body)
	}
}
