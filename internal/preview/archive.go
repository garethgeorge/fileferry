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
	// fall through to a raw download.
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

// ServeHTTP reads f, lists its entries, and writes an HTML listing. Content
// that does not parse as the expected archive (e.g. a plain .gz file) is served
// raw instead.
func (a *archive) ServeHTTP(w http.ResponseWriter, r *http.Request, f File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, maxArchivePreviewSize))
	if err != nil {
		return err
	}

	kind, entries, truncated, err := listArchive(strings.ToLower(f.Ext), data)
	if errors.Is(err, errNotArchive) {
		return serveRawBytes(w, r, f, data)
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

// listArchive dispatches on extension. It returns errNotArchive when the bytes
// are not the expected format so the caller can fall back to raw serving.
func listArchive(ext string, data []byte) (kind string, entries []archiveEntry, truncated bool, err error) {
	switch ext {
	case "zip":
		entries, truncated, err = listZip(data)
		return "zip", entries, truncated, err
	default: // tar, tgz, gz
		gzipped := ext != "tar"
		entries, truncated, err = listTar(data, gzipped)
		kind = "tar"
		if gzipped {
			kind = "tar.gz"
		}
		return kind, entries, truncated, err
	}
}

func listZip(data []byte) (entries []archiveEntry, truncated bool, err error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
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

func listTar(data []byte, gzipped bool) (entries []archiveEntry, truncated bool, err error) {
	var src io.Reader = bytes.NewReader(data)
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

// serveRawBytes serves already-read content as a plain download when it turned
// out not to be a previewable archive.
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
