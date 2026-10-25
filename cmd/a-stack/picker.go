package main

import (
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// sessionRow is one Session as the Operator shortcuts list it. It holds IDs and
// names only, never credentials.
type sessionRow struct {
	WorkspaceID   string    `json:"workspace_id"`
	WorkspacePath string    `json:"workspace_path"`
	SessionID     string    `json:"session_id"`
	Name          string    `json:"name"`
	Closed        bool      `json:"closed"`
	Tasks         int       `json:"tasks"`
	NeedsYou      int       `json:"needs_you"`
	ChangedAt     time.Time `json:"changed_at"`
	CurrentFolder bool      `json:"current_folder"`
}

// sortRows puts the most recently changed Session first.
func sortRows(rows []sessionRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].ChangedAt.Equal(rows[j].ChangedAt) {
			return rows[i].ChangedAt.After(rows[j].ChangedAt)
		}
		return rows[i].SessionID < rows[j].SessionID
	})
}

// filterRows keeps the order. The query matches the Session name or the
// Workspace folder, without regard to case.
func filterRows(rows []sessionRow, currentOnly bool, query string) []sessionRow {
	query = strings.ToLower(query)
	out := []sessionRow{}
	for _, r := range rows {
		if currentOnly && !r.CurrentFolder {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(r.Name+"\x00"+r.WorkspacePath), query) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func relativeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d"
}

func shortenHome(path, home string) string {
	if home == "" || home == "/" {
		return path
	}
	if rest, ok := pathBelow(path, home, windowsPaths); ok {
		return "~" + rest
	}
	return path
}

// windowsPaths is true on Windows, where paths use \ and either case.
var windowsPaths = runtime.GOOS == "windows"

// pathBelow returns the rest of path after root, such as /sub, when path is
// root or a path below it. With windows, / and \ both separate, and case
// does not matter.
func pathBelow(path, root string, windows bool) (rest string, ok bool) {
	sep, equal := "/", func(a, b string) bool { return a == b }
	if windows {
		sep, equal = `\`, strings.EqualFold
		path, root = strings.ReplaceAll(path, "/", sep), strings.ReplaceAll(root, "/", sep)
	}
	root = strings.TrimSuffix(root, sep)
	if len(path) < len(root) || !equal(path[:len(root)], root) {
		return "", false
	}
	rest = path[len(root):]
	return rest, rest == "" || strings.HasPrefix(rest, sep)
}

// printable keeps names and paths from the service from moving the cursor or
// changing the terminal: every control or unprintable rune becomes U+FFFD.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return '\uFFFD'
		}
		return r
	}, s)
}

func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	return string(r[:width-1]) + "…"
}

func truncateLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return "…" + string(r[len(r)-width+1:])
}

func rowState(r sessionRow) string {
	if r.Closed {
		return "closed"
	}
	return "open"
}

func rowCounts(r sessionRow) string {
	text := fmt.Sprintf("%d tasks", r.Tasks)
	if r.Tasks == 1 {
		text = "1 task"
	}
	if r.NeedsYou > 0 {
		text += fmt.Sprintf(", %d needs you", r.NeedsYou)
	}
	return text
}

// formatRow fits one row into width columns, with the age at the right edge.
func formatRow(r sessionRow, selected bool, home string, now time.Time, width int) string {
	mark := "  "
	if selected {
		mark = "> "
	}
	age := relativeAge(now.Sub(r.ChangedAt))
	name := truncate(printable(r.Name), 40)
	tail := "  " + rowState(r) + "  " + rowCounts(r)
	// The folder gets the space that is left and loses its start first, so
	// the folder name stays visible.
	folderRoom := width - utf8.RuneCountInString(mark+name+tail+age) - 3
	left := mark + name + "  " + truncateLeft(printable(shortenHome(r.WorkspacePath, home)), folderRoom) + tail
	room := width - utf8.RuneCountInString(age) - 1
	if room < 1 {
		return truncate(left, width)
	}
	left = truncate(left, room)
	return left + strings.Repeat(" ", width-utf8.RuneCountInString(left)-utf8.RuneCountInString(age)) + age
}

// picker is the state of the interactive Session list. The terminal loop only
// feeds it keys and prints what render returns.
type picker struct {
	rows     []sessionRow
	all      bool
	query    string
	selected int
}

type pickAction int

const (
	pickContinue pickAction = iota
	pickChoose
	pickCancel
)

func newPicker(rows []sessionRow) *picker {
	p := &picker{rows: rows, all: true}
	for _, r := range rows {
		if r.CurrentFolder {
			p.all = false
			break
		}
	}
	return p
}

func (p *picker) visible() []sessionRow { return filterRows(p.rows, !p.all, p.query) }

// chosen returns the selected row; call it only after pickChoose.
func (p *picker) chosen() sessionRow { return p.visible()[p.selected] }

// input applies each key of one terminal read in order, so an arrow key and
// Enter or a paste in one read all count. It stops at Enter or a cancel.
func (p *picker) input(in []byte) pickAction {
	for _, k := range splitKeys(in) {
		if action := p.key(k); action != pickContinue {
			return action
		}
	}
	return pickContinue
}

// splitKeys cuts terminal input into keys: an escape sequence (CSI such as
// ESC [ 1 ; 5 C, or SS3 such as ESC O A), a lone ESC, one control byte or
// one UTF-8 character.
func splitKeys(in []byte) [][]byte {
	keys := [][]byte{}
	for i := 0; i < len(in); {
		n := 1
		switch {
		case in[i] == 0x1b && i+1 < len(in) && in[i+1] == '[':
			n = 2
			for i+n < len(in) && in[i+n] >= 0x20 && in[i+n] <= 0x3f {
				n++
			}
			if i+n < len(in) && in[i+n] >= 0x40 && in[i+n] <= 0x7e {
				n++
			}
		case in[i] == 0x1b && i+2 < len(in) && in[i+1] == 'O':
			n = 3
		case in[i] >= 0x80:
			_, n = utf8.DecodeRune(in[i:])
		}
		keys = append(keys, in[i:i+n])
		i += n
	}
	return keys
}

// key handles one key.
func (p *picker) key(in []byte) pickAction {
	switch string(in) {
	case "\x1b[A", "\x1bOA", "\x10":
		if p.selected > 0 {
			p.selected--
		}
		return pickContinue
	case "\x1b[B", "\x1bOB", "\x0e":
		if p.selected < len(p.visible())-1 {
			p.selected++
		}
		return pickContinue
	case "\t":
		p.all = !p.all
		p.selected = 0
		return pickContinue
	case "\r", "\n":
		if len(p.visible()) > 0 {
			return pickChoose
		}
		return pickContinue
	case "\x1b", "\x03":
		return pickCancel
	case "\x7f", "\x08":
		if p.query != "" {
			_, size := utf8.DecodeLastRuneInString(p.query)
			p.query = p.query[:len(p.query)-size]
			p.selected = 0
		}
		return pickContinue
	}
	if len(in) == 0 || in[0] == 0x1b {
		return pickContinue
	}
	changed := false
	for _, r := range string(in) {
		if r != utf8.RuneError && unicode.IsPrint(r) {
			p.query += string(r)
			changed = true
		}
	}
	if changed {
		p.selected = 0
	}
	return pickContinue
}

const (
	inverse = "\x1b[7m"
	plain   = "\x1b[0m"
)

// render returns the picker lines for a terminal of the given size. No line is
// wider than width-1 columns, so the terminal never wraps one.
func (p *picker) render(width, height int, home string, now time.Time) []string {
	width--
	if width < 20 {
		width = 20
	}
	scope := "Current Folder"
	switches := inverse + "Current Folder" + plain + " | All"
	if p.all {
		scope = "All"
		switches = "Current Folder | " + inverse + "All" + plain
	}
	lines := []string{
		truncate("Resume Session ("+scope+")", width),
		switches + truncate("   Tab to switch", width-len("Current Folder | All")),
		"> " + truncate(printable(p.query), width-2),
	}
	rows := p.visible()
	window := height - len(lines) - 2
	if window > 15 {
		window = 15
	}
	if window < 1 {
		window = 1
	}
	start := 0
	if p.selected >= window {
		start = p.selected - window + 1
	}
	for i := start; i < len(rows) && i < start+window; i++ {
		line := formatRow(rows[i], i == p.selected, home, now, width)
		if i == p.selected {
			line = inverse + line + plain
		}
		lines = append(lines, line)
	}
	position := 0
	if len(rows) == 0 {
		lines = append(lines, "  No matching Session")
	} else {
		position = p.selected + 1
	}
	return append(lines, truncate(fmt.Sprintf("(%d/%d)  Up/Down move, Enter opens, Esc cancels", position, len(rows)), width))
}
