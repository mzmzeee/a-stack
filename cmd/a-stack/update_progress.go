package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// progress shows the steps of an update on a terminal: a line for each
// step, and a bar that redraws itself with \r while the archive downloads.
// It uses no ANSI escapes, so every terminal of Windows and macOS draws it.
// A nil progress prints nothing.
type progress struct {
	w     io.Writer
	bar   bool // draw the bar, else only the step lines
	cols  int  // terminal width
	now   func() time.Time
	total int64 // -1 when unknown
	done  int64
	start time.Time
	drawn time.Time
	width int  // length of the drawn frame
	open  bool // a frame is drawn and its line not ended
}

// stderrTerminal reports whether w is a terminal. Tests replace it.
var stderrTerminal = func(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTerminal(f)
}

// newProgress returns the progress of an update on stderr, or nil when
// stderr is not a terminal or with --json, so that the output stays as it
// is for scripts. With NO_COLOR or TERM=dumb it prints the steps but no bar.
func newProgress(stderr io.Writer, machine bool) *progress {
	if machine || !stderrTerminal(stderr) {
		return nil
	}
	cols := 80
	if f, ok := stderr.(*os.File); ok {
		cols, _ = terminalSize(f)
	}
	return &progress{w: stderr, bar: os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb", cols: cols, now: time.Now}
}

// step ends the bar's line and prints line.
func (p *progress) step(line string) {
	if p == nil {
		return
	}
	p.end()
	_, _ = io.WriteString(p.w, line+"\n")
}

// download shows the download of body, of total bytes or -1, while it is
// read. The caller calls end when it stops reading.
func (p *progress) download(body io.Reader, name string, total int64) io.Reader {
	if p == nil {
		return body
	}
	if !p.bar {
		p.step("Downloading " + name)
		return body
	}
	p.total, p.done, p.start = total, 0, p.now()
	p.draw(p.start)
	return io.TeeReader(body, p)
}

// Write counts downloaded bytes and redraws the bar at most 10 times a second.
func (p *progress) Write(b []byte) (int, error) {
	p.done += int64(len(b))
	if t := p.now(); t.Sub(p.drawn) >= 100*time.Millisecond {
		p.draw(t)
	}
	return len(b), nil
}

// end draws the last frame and ends its line, also after a failed download,
// so that nothing else is printed on a half-drawn line.
func (p *progress) end() {
	if p == nil || !p.open {
		return
	}
	p.draw(p.now())
	_, _ = io.WriteString(p.w, "\n")
	p.open, p.width = false, 0
}

func (p *progress) draw(t time.Time) {
	mb := func(n int64) string { return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) }
	text := "  " + mb(p.done) + " MB"
	if p.total > 0 {
		text = "  " + mb(p.done) + " / " + mb(p.total) + " MB"
	}
	if s := t.Sub(p.start).Seconds(); s >= 0.1 {
		rate := float64(p.done) / s
		if rate >= 1e6 {
			text += "  " + strconv.FormatFloat(rate/1e6, 'f', 1, 64) + " MB/s"
		} else {
			text += "  " + strconv.Itoa(int(rate/1e3)) + " kB/s"
		}
	}
	line := "Downloading"
	if p.total > 0 {
		percent := min(p.done*100/p.total, 100)
		// The bar leaves room for the longest text, "  256.0 / 256.0 MB  999.9 MB/s".
		if n := min(20, p.cols-1-len(line)-8-30); n >= 5 {
			fill := int(int64(n) * percent / 100)
			line += " [" + strings.Repeat("#", fill) + strings.Repeat("-", n-fill) + "]"
		}
		line += fmt.Sprintf(" %3d%%", percent)
	}
	line += text
	// A line as wide as the terminal wraps, and \r then goes back only to
	// the start of its last row.
	if len(line) > p.cols-1 {
		line = line[:max(p.cols-1, 0)]
	}
	// Spaces cover the rest of a longer frame before.
	_, _ = io.WriteString(p.w, "\r"+line+strings.Repeat(" ", max(p.width-len(line), 0)))
	p.width, p.drawn, p.open = max(p.width, len(line)), t, true
}
