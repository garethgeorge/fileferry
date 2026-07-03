package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// prepareFile turns a filesystem path into an upload body. A regular file is
// opened and streamed as-is; a directory is compressed into a temporary archive
// (a .zip on Windows, a .tar.gz everywhere else) first. The returned cleanup
// closes the file handle and removes any temporary archive, and must be called
// once the upload finishes.
func prepareFile(path string, quiet bool) (body io.Reader, size int64, filename string, cleanup func(), err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, -1, "", nil, err
	}
	if info.IsDir() {
		f, sz, name, err := archiveDir(path, quiet)
		if err != nil {
			return nil, -1, "", nil, fmt.Errorf("archiving %s: %w", path, err)
		}
		return f, sz, name, func() { f.Close(); os.Remove(f.Name()) }, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, -1, "", nil, err
	}
	return f, info.Size(), filepath.Base(path), func() { f.Close() }, nil
}

// archiveDir compresses dir into a temporary archive and returns it rewound and
// ready to read, along with its size and the filename to upload it under. The
// format follows the platform: .zip on Windows, .tar.gz elsewhere. A copy bar
// tracks the source bytes read while the archive is built. The caller owns the
// returned file and must close and remove it (see prepareFile's cleanup).
func archiveDir(dir string, quiet bool) (*os.File, int64, string, error) {
	// Pre-sum the regular-file bytes so the copy bar has a real total to fill.
	var total int64
	if err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	}); err != nil {
		return nil, 0, "", err
	}

	useZip := runtime.GOOS == "windows"
	ext := ".tar.gz"
	if useZip {
		ext = ".zip"
	}

	tmp, err := os.CreateTemp("", "ferryupload-*"+ext)
	if err != nil {
		return nil, 0, "", err
	}

	bar := newCopyBar(total, "archiving", quiet)
	if useZip {
		err = writeZip(tmp, dir, bar)
	} else {
		err = writeTarGz(tmp, dir, bar)
	}
	bar.finish()
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, 0, "", err
	}

	size, err := tmp.Seek(0, io.SeekEnd)
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, 0, "", err
	}

	base := filepath.Base(filepath.Clean(dir))
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "archive"
	}
	return tmp, size, base + ext, nil
}

// writeTarGz writes dir as a gzip-compressed tar into dst, storing every entry
// under the directory's own name so it extracts into a single top-level folder.
// Only directories and regular files are archived; symlinks and special files
// are skipped.
func writeTarGz(dst io.Writer, dir string, bar *copyBar) error {
	gz := gzip.NewWriter(dst)
	tw := tar.NewWriter(gz)
	root := filepath.Clean(dir)
	base := filepath.Base(root)

	walkErr := filepath.Walk(root, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		name, err := archiveName(root, base, p)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			return copyRegular(tw, p, bar)
		}
		return nil
	})
	if walkErr != nil {
		tw.Close()
		gz.Close()
		return walkErr
	}
	if err := tw.Close(); err != nil {
		gz.Close()
		return err
	}
	return gz.Close()
}

// writeZip writes dir as a deflate-compressed zip into dst, mirroring
// writeTarGz's single-top-level-folder layout and file selection.
func writeZip(dst io.Writer, dir string, bar *copyBar) error {
	zw := zip.NewWriter(dst)
	root := filepath.Clean(dir)
	base := filepath.Base(root)

	walkErr := filepath.Walk(root, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}
		name, err := archiveName(root, base, p)
		if err != nil {
			return err
		}
		if info.IsDir() {
			_, err := zw.Create(name + "/")
			return err
		}
		hdr, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		return copyRegular(w, p, bar)
	})
	if walkErr != nil {
		zw.Close()
		return walkErr
	}
	return zw.Close()
}

// archiveName returns the slash-separated path to store p under, rooted at the
// archived directory's own base name (e.g. "myfolder/sub/file.txt").
func archiveName(root, base, p string) (string, error) {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return base, nil
	}
	return filepath.ToSlash(filepath.Join(base, rel)), nil
}

// copyRegular streams one regular file into w through the copy bar, closing it
// immediately so a deep tree never holds more than one file handle open.
func copyRegular(w io.Writer, path string, bar *copyBar) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, bar.wrap(f))
	return err
}
