package store

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type ListEntry struct {
	ID         string    `json:"id"`
	Slug       string    `json:"slug"`
	Ext        string    `json:"ext"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploadedAt"`
}

// List returns named files (those with a slug) newest-first. The cursor is
// "<month-dir>/<filename>" of the last returned entry; resumption is by
// strict less-than comparison, so a deleted cursor file is harmless. An empty
// nextCursor means no more pages.
func (s *Store) List(cursor string, limit int) (entries []ListEntry, nextCursor string, err error) {
	if limit <= 0 {
		limit = 50
	}
	var curMonth, curName string
	if i := strings.IndexByte(cursor, '/'); i > 0 {
		curMonth, curName = cursor[:i], cursor[i+1:]
	}

	dirs, err := os.ReadDir(s.dataDir)
	if err != nil {
		return nil, "", err
	}
	var months []string
	for _, d := range dirs {
		if d.IsDir() && isMonthDir(d.Name()) {
			months = append(months, d.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(months)))

	for _, month := range months {
		if curMonth != "" && month > curMonth {
			continue
		}
		files, err := os.ReadDir(filepath.Join(s.dataDir, month))
		if err != nil {
			continue
		}
		names := make([]string, 0, len(files))
		for _, f := range files {
			if f.IsDir() || strings.HasSuffix(f.Name(), ".tmp") {
				continue
			}
			names = append(names, f.Name())
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))

		for _, name := range names {
			if month == curMonth && name >= curName {
				continue
			}
			id, err := ParseID(name)
			if err != nil || id.Slug == "" {
				continue
			}
			info, err := os.Stat(filepath.Join(s.dataDir, month, name))
			if err != nil {
				continue
			}
			entries = append(entries, ListEntry{
				ID:         name,
				Slug:       id.Slug,
				Ext:        id.Ext,
				Size:       info.Size(),
				UploadedAt: id.UploadedAt(),
			})
			if len(entries) == limit {
				return entries, month + "/" + name, nil
			}
		}
	}
	return entries, "", nil
}
