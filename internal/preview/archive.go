package preview

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// maxArchivePreviewSize caps the archive size we will read; larger files
	// fall through to a raw download. It also bounds how much we buffer in
	// memory for sources that don't support random access (see ServeHTTP).
	maxArchivePreviewSize = 128 << 20 // 128 MiB
	// maxArchiveEntries caps how many entries we list, bounding the response.
	maxArchiveEntries = 10000
	// maxArchiveScan caps the number of decompressed bytes read while walking a
	// tar so a decompression bomb cannot make us read unbounded data.
	maxArchiveScan = 1 << 30 // 1 GiB
)

//go:embed archive.html
var archiveTemplateSource string

var archiveTemplate = template.Must(template.New("archive").Parse(archiveTemplateSource))

// errNotArchive means the content did not parse as the expected archive; the
// caller serves it raw instead.
var errNotArchive = errors.New("not a readable archive")

// NewArchive returns a Previewer that lists the contents of zip, tar, and
// tar.gz archives without extracting them. Only entry metadata (name, size) is
// read, so archive contents are never written to disk or to the response.
func NewArchive() Previewer {
	return &archive{}
}

type archive struct{}

type archiveEntry struct {
	Name    string
	SizeStr string
	IsDir   bool
}

// Matches reports whether f looks like a supported archive small enough to
// scan. tar.gz uploads are stored with a single "gz" extension, so "gz" is
// accepted and validated by content in ServeHTTP.
func (a *archive) Matches(f File) bool {
	if f.Size <= 0 || f.Size > maxArchivePreviewSize {
		return false
	}
	switch strings.ToLower(f.Ext) {
	case "zip", "tar", "tgz", "gz":
		return true
	}
	return false
}

// ServeHTTP reads f and writes an HTML listing of its entries. When the
// underlying content supports random access (true for any file backed by
// local disk) it is scanned directly, without buffering the archive into
// memory: zip reads only its central directory via io.ReaderAt, and tar/gzip
// are walked as a stream. Sources that don't support random access (e.g. a
// decrypting stream) fall back to buffering up to maxArchivePreviewSize, same
// as before. Content that does not parse as the expected archive (e.g. a
// plain .gz file) is served raw instead.
func (a *archive) ServeHTTP(w http.ResponseWriter, r *http.Request, f File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	ext := strings.ToLower(f.Ext)
	ra, hasReaderAt := rc.(io.ReaderAt)
	rs, seekable := rc.(io.ReadSeeker)

	// rawFallback rewinds a seekable source and serves it as a plain
	// download. Only called from branches that established seekable == true,
	// so rs is always non-nil where it's used.
	rawFallback := func() error {
		if _, serr := rs.Seek(0, io.SeekStart); serr != nil {
			return serr
		}
		return serveRawStream(w, r, f, rs)
	}

	var kind string
	var entries []archiveEntry
	var truncated bool

	switch {
	case ext == "zip" && hasReaderAt && seekable:
		kind = "zip"
		entries, truncated, err = listZip(ra, f.Size)
		if errors.Is(err, errNotArchive) {
			return rawFallback()
		}
	case seekable:
		kind, entries, truncated, err = listTarStream(ext, io.LimitReader(rc, maxArchivePreviewSize))
		if errors.Is(err, errNotArchive) {
			return rawFallback()
		}
	default:
		data, rerr := io.ReadAll(io.LimitReader(rc, maxArchivePreviewSize))
		if rerr != nil {
			return rerr
		}
		if ext == "zip" {
			kind = "zip"
			entries, truncated, err = listZip(bytes.NewReader(data), int64(len(data)))
		} else {
			kind, entries, truncated, err = listTarStream(ext, bytes.NewReader(data))
		}
		if errors.Is(err, errNotArchive) {
			return serveRawBytes(w, r, f, data)
		}
	}
	if err != nil {
		return err
	}

	var page bytes.Buffer
	pageData := struct {
		ID        string
		Kind      string
		TotalSize string
		Count     int
		Truncated bool
		Entries   []archiveEntry
	}{
		ID:        f.ID,
		Kind:      kind,
		TotalSize: humanSize(f.Size),
		Count:     len(entries),
		Truncated: truncated,
		Entries:   entries,
	}
	if err := archiveTemplate.Execute(&page, pageData); err != nil {
		return err
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "script-src 'none'")
	_, err = w.Write(page.Bytes())
	return err
}

// listTarStream dispatches tar/tgz/gz by extension and walks the entries as a
// stream; it never buffers the whole archive.
func listTarStream(ext string, src io.Reader) (kind string, entries []archiveEntry, truncated bool, err error) {
	gzipped := ext != "tar"
	entries, truncated, err = listTar(src, gzipped)
	kind = "tar"
	if gzipped {
		kind = "tar.gz"
	}
	return kind, entries, truncated, err
}

// listZip reads only the central directory (via ra), so memory use is
// proportional to the entry count, not the archive size.
func listZip(ra io.ReaderAt, size int64) (entries []archiveEntry, truncated bool, err error) {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return nil, false, errNotArchive
	}
	for _, zf := range zr.File {
		if len(entries) >= maxArchiveEntries {
			return entries, true, nil
		}
		info := zf.FileInfo()
		entries = append(entries, archiveEntry{
			// Directory names already carry a trailing "/"; the template adds
			// its own, so strip it here for both files and dirs.
			Name:    strings.TrimSuffix(zf.Name, "/"),
			SizeStr: humanSize(int64(zf.UncompressedSize64)),
			IsDir:   info.IsDir(),
		})
	}
	return entries, false, nil
}

func listTar(src io.Reader, gzipped bool) (entries []archiveEntry, truncated bool, err error) {
	if gzipped {
		gz, gerr := gzip.NewReader(src)
		if gerr != nil {
			return nil, false, errNotArchive
		}
		defer gz.Close()
		src = gz
	}
	tr := tar.NewReader(io.LimitReader(src, maxArchiveScan))
	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			return entries, false, nil
		}
		if terr != nil {
			// A failure on the very first header means it is not a tar at all;
			// otherwise we have a partial (e.g. size-capped) listing.
			if len(entries) == 0 {
				return nil, false, errNotArchive
			}
			return entries, true, nil
		}
		if len(entries) >= maxArchiveEntries {
			return entries, true, nil
		}
		entries = append(entries, archiveEntry{
			Name:    strings.TrimSuffix(hdr.Name, "/"),
			SizeStr: humanSize(hdr.Size),
			IsDir:   hdr.FileInfo().IsDir(),
		})
	}
}

// serveRawStream serves a seekable source (already rewound to the start) as a
// plain download, supporting range requests.
func serveRawStream(w http.ResponseWriter, r *http.Request, f File, rs io.ReadSeeker) error {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if f.MimeType != "" {
		w.Header().Set("Content-Type", f.MimeType)
	}
	http.ServeContent(w, r, f.ID, time.Time{}, rs)
	return nil
}

// serveRawBytes serves already-buffered content as a plain download when it
// turned out not to be a previewable archive.
func serveRawBytes(w http.ResponseWriter, r *http.Request, f File, data []byte) error {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if f.MimeType != "" {
		w.Header().Set("Content-Type", f.MimeType)
	}
	http.ServeContent(w, r, f.ID, time.Time{}, bytes.NewReader(data))
	return nil
}

// humanSize formats a byte count like "1.5 MB".
func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	val := float64(n) / 1024
	i := 0
	for val >= 1024 && i < len(units)-1 {
		val /= 1024
		i++
	}
	if val < 10 {
		return fmt.Sprintf("%.1f %s", val, units[i])
	}
	return fmt.Sprintf("%.0f %s", val, units[i])
}
