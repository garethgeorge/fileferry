package main

import (
	"io"
	"os"
	"time"

	"github.com/schollz/progressbar/v3"
	"golang.org/x/term"
)

// progressThreshold is how many bytes must be read before a progress bar
// appears, so small pastes and files upload silently.
const progressThreshold = 8 << 20 // 8 MiB

// progressReader wraps an io.Reader and renders a progress bar on stderr once
// the transfer crosses progressThreshold bytes. total is the known size, or
// -1 for an unknown-length source (stdin), which renders as a byte-counting
// spinner instead of a percentage.
type progressReader struct {
	r       io.Reader
	total   int64
	read    int64
	enabled bool
	bar     *progressbar.ProgressBar
}

func newProgressReader(r io.Reader, total int64, quiet bool) *progressReader {
	return &progressReader{
		r:       r,
		total:   total,
		enabled: !quiet && term.IsTerminal(int(os.Stderr.Fd())),
	}
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.read += int64(n)
		justCreated := false
		if p.bar == nil && p.enabled && p.read >= progressThreshold {
			p.bar = progressbar.NewOptions64(p.total,
				progressbar.OptionSetWriter(os.Stderr),
				progressbar.OptionSetDescription("uploading"),
				progressbar.OptionShowBytes(true),
				progressbar.OptionSetWidth(30),
				progressbar.OptionThrottle(100*time.Millisecond),
				progressbar.OptionClearOnFinish(),
				progressbar.OptionFullWidth(),
			)
			// Seed the bar with bytes already read before it was created, so
			// it doesn't restart the percentage/counter from zero.
			p.bar.Set64(p.read)
			justCreated = true
		}
		if p.bar != nil && !justCreated {
			p.bar.Add64(int64(n))
		}
	}
	if err != nil && p.bar != nil {
		p.bar.Finish()
	}
	return n, err
}
