package store

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
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

// candidate is a file within a single month directory, carrying everything
// needed to order it precisely: the day parsed from its ID (coarse, matches
// what's visible in the ID/URL) and its filesystem mtime (fine-grained
// tiebreak for files sharing a day, since IDs deliberately don't encode more
// than day-granularity).
type candidate struct {
	name  string
	id    FileID
	mtime int64 // UnixNano
	size  int64
}

// List returns uploaded files newest-first, ordered by day (as encoded in the
// ID) and, for files sharing a day, by actual upload time (filesystem mtime)
// — a plain name sort would otherwise order same-day files by their random
// nonce. The cursor is "<month-dir>/<mtime>/<filename>" of the last returned
// entry; resumption compares against that triple without touching disk, so a
// deleted cursor file is harmless.
func (s *Store) List(cursor string, limit int) (entries []ListEntry, nextCursor string, err error) {
	if limit <= 0 {
		limit = 50
	}
	var curMonth, curName string
	var curDay int
	var curMtime int64
	if parts := strings.SplitN(cursor, "/", 3); len(parts) == 3 {
		curMonth = parts[0]
		curName = parts[2]
		curMtime, _ = strconv.ParseInt(parts[1], 10, 64)
		if id, err := ParseID(curName); err == nil {
			curDay = id.Day
		}
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
		candidates := make([]candidate, 0, len(files))
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			id, err := ParseID(f.Name())
			if err != nil {
				continue
			}
			info, err := os.Stat(filepath.Join(s.dataDir, month, f.Name()))
			if err != nil {
				continue
			}
			candidates = append(candidates, candidate{
				name:  f.Name(),
				id:    id,
				mtime: info.ModTime().UnixNano(),
				size:  info.Size(),
			})
		}
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if a.id.Day != b.id.Day {
				return a.id.Day > b.id.Day
			}
			if a.mtime != b.mtime {
				return a.mtime > b.mtime
			}
			return a.name > b.name
		})

		for _, c := range candidates {
			if month == curMonth {
				var atOrBeforeCursor bool
				switch {
				case c.id.Day != curDay:
					atOrBeforeCursor = c.id.Day > curDay
				case c.mtime != curMtime:
					atOrBeforeCursor = c.mtime > curMtime
				default:
					atOrBeforeCursor = c.name >= curName
				}
				if atOrBeforeCursor {
					continue
				}
			}
			entries = append(entries, ListEntry{
				ID:         c.name,
				Slug:       c.id.Slug,
				Ext:        c.id.Ext,
				Size:       c.size,
				UploadedAt: c.id.UploadedAt(),
			})
			if len(entries) == limit {
				next := month + "/" + strconv.FormatInt(c.mtime, 10) + "/" + c.name
				return entries, next, nil
			}
		}
	}
	return entries, "", nil
}
