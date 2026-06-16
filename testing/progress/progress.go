// Package progress prints a lightweight tqdm-style progress bar for the corpus
// harnesses, so a long run visibly advances instead of looking stuck. It
// animates in place on a terminal and falls back to occasional plain lines when
// output is redirected (a log or CI). It always writes to stderr, so it never
// mixes into a harness's report on stdout.
package progress

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Bar is a counter-based progress bar. Inc is safe to call from multiple
// goroutines (the parallel corpus walker does). The zero total is a no-op.
type Bar struct {
	label    string
	total    int
	tty      bool
	interval time.Duration

	mu    sync.Mutex
	done  int
	start time.Time
	last  time.Time
}

// New starts a bar labeled label for total steps.
func New(label string, total int) *Bar {
	tty := isTTY(os.Stderr)
	interval := 100 * time.Millisecond
	if !tty {
		interval = 2 * time.Second // avoid spamming a redirected log
	}
	return &Bar{label: label, total: total, tty: tty, interval: interval, start: time.Now()}
}

// Inc advances the bar by one step, repainting at most once per interval.
func (b *Bar) Inc() {
	if b.total <= 0 {
		return
	}
	b.mu.Lock()
	b.done++
	now := time.Now()
	if now.Sub(b.last) >= b.interval {
		b.last = now
		b.render(false)
	}
	b.mu.Unlock()
}

// Finish paints the completed bar and ends its line.
func (b *Bar) Finish() {
	if b.total <= 0 {
		return
	}
	b.mu.Lock()
	b.render(true)
	b.mu.Unlock()
}

// render paints the current state; the caller holds the lock.
func (b *Bar) render(final bool) {
	done := min(b.done, b.total)
	pct := done * 100 / b.total
	elapsed := time.Since(b.start).Round(time.Second)

	if !b.tty {
		fmt.Fprintf(os.Stderr, "%s: %d%% (%d/%d) %s\n", b.label, pct, done, b.total, elapsed)
		return
	}

	const width = 24
	fill := pct * width / 100
	bar := strings.Repeat("=", fill) + strings.Repeat(" ", width-fill)
	tail := fmt.Sprintf("%s elapsed", elapsed)
	if !final && done > 0 {
		eta := time.Duration(float64(elapsed) / float64(done) * float64(b.total-done)).Round(time.Second)
		tail = fmt.Sprintf("eta %s", eta)
	}
	// Trailing spaces clear any leftover from a longer previous line.
	fmt.Fprintf(os.Stderr, "\r%s [%s] %3d%% %d/%d %s   ", b.label, bar, pct, done, b.total, tail)
	if final {
		fmt.Fprintln(os.Stderr)
	}
}

// isTTY reports whether f is a character device (an interactive terminal).
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
