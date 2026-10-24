package main

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPickerAgesPathsAndPrintableText(t *testing.T) {
	for d, want := range map[time.Duration]string{0: "now", 59 * time.Second: "now", 7 * time.Minute: "7m", 2*time.Hour + 59*time.Minute: "2h", 25 * time.Hour: "1d", 400 * 24 * time.Hour: "400d"} {
		if got := relativeAge(d); got != want {
			t.Fatalf("relativeAge(%v)=%q want %q", d, got, want)
		}
	}
	for _, c := range [][3]string{{"/home/a/code/x", "/home/a", "~/code/x"}, {"/home/a", "/home/a", "~"}, {"/home/ab/x", "/home/a", "/home/ab/x"}, {"/srv/x", "", "/srv/x"}, {"/x", "/", "/x"}} {
		if got := shortenHome(c[0], c[1]); got != c[2] {
			t.Fatalf("shortenHome(%q,%q)=%q", c[0], c[1], got)
		}
	}
	if got := printable("a\x1b[31mb\u202e\tc\xff"); strings.ContainsAny(got, "\x1b\t\u202e") || !strings.HasPrefix(got, "a\uFFFD[31mb") {
		t.Fatalf("printable kept control text: %q", got)
	}
	if !within("/p/a/sub", "/p/a") || !within("/p/a", "/p/a") || within("/p/ab", "/p/a") || !within("/x", "/") || within("", "/p") {
		t.Fatal("within")
	}
}

func rowsFixture(now time.Time) []sessionRow {
	return []sessionRow{
		{SessionID: "a", Name: "Alpha", WorkspacePath: "/home/u/code/alpha", ChangedAt: now.Add(-2 * time.Hour), CurrentFolder: true, Tasks: 3, NeedsYou: 1},
		{SessionID: "b", Name: "Beta build", WorkspacePath: "/home/u/code/beta", ChangedAt: now.Add(-7 * time.Minute), Closed: true, Tasks: 1},
		{SessionID: "c", Name: "Gamma", WorkspacePath: "/home/u/code/alpha", ChangedAt: now.Add(-3 * 24 * time.Hour), CurrentFolder: true},
	}
}

func TestPickerSortFilterAndRows(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	rows := rowsFixture(now)
	sortRows(rows)
	if rows[0].SessionID != "b" || rows[1].SessionID != "a" || rows[2].SessionID != "c" {
		t.Fatal("rows are not newest first")
	}
	if got := filterRows(rows, true, ""); len(got) != 2 || got[0].SessionID != "a" {
		t.Fatal("current folder filter")
	}
	if got := filterRows(rows, false, "BETA"); len(got) != 1 || got[0].SessionID != "b" {
		t.Fatal("case-insensitive name filter")
	}
	if got := filterRows(rows, false, "code/alpha"); len(got) != 2 {
		t.Fatal("folder filter")
	}
	line := formatRow(rows[1], true, "/home/u", now, 70)
	if utf8.RuneCountInString(line) != 70 || !strings.HasPrefix(line, "> Alpha  ~/code/alpha  open  3 tasks, 1 needs you") || !strings.HasSuffix(line, " 2h") {
		t.Fatalf("row %q", line)
	}
	line = formatRow(rows[0], false, "/home/u", now, 40)
	if utf8.RuneCountInString(line) != 40 || !strings.HasPrefix(line, "  Beta build  ") || !strings.Contains(line, "closed  1 task") || !strings.HasSuffix(line, " 7m") {
		t.Fatalf("narrow row %q", line)
	}
	long := sessionRow{Name: strings.Repeat("n", 100), WorkspacePath: "/home/u/" + strings.Repeat("d", 100) + "/project", ChangedAt: now}
	line = formatRow(long, false, "/home/u", now, 90)
	if utf8.RuneCountInString(line) != 90 || !strings.Contains(line, "  …ddd") || !strings.Contains(line, "/project  open") || !strings.HasSuffix(line, " now") {
		t.Fatalf("long row %q", line)
	}
	if got := formatRow(long, false, "", now, 3); utf8.RuneCountInString(got) > 3 {
		t.Fatal("tiny width")
	}
}

func TestPickerKeysScopeAndRender(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	rows := rowsFixture(now)
	sortRows(rows)
	p := newPicker(rows)
	if p.all || len(p.visible()) != 2 {
		t.Fatal("picker does not start in the current folder")
	}
	if newPicker(rows[:1]).all != true {
		t.Fatal("picker without current folder rows starts in All")
	}
	for _, k := range []string{"\x1b[B", "\x0e", "\x0e"} {
		p.key([]byte(k))
	}
	if p.selected != 1 {
		t.Fatal("down moves and stops at the last row")
	}
	p.key([]byte("\x10"))
	p.key([]byte("\x1b[A"))
	if p.selected != 0 {
		t.Fatal("up moves and stops at the first row")
	}
	p.key([]byte("\x0e"))
	p.key([]byte("\t"))
	if !p.all || p.selected != 0 || len(p.visible()) != 3 {
		t.Fatal("tab switches to All and resets the selection")
	}
	p.key([]byte("gaX"))
	if p.query != "gaX" || len(p.visible()) != 0 || p.key([]byte("\r")) != pickContinue {
		t.Fatal("typing filters; Enter on an empty list does nothing")
	}
	lines := p.render(60, 20, "/home/u", now)
	if !strings.HasPrefix(lines[len(lines)-1], "(0/0)") || !strings.Contains(strings.Join(lines, "\n"), "No matching Session") {
		t.Fatal("empty render")
	}
	p.key([]byte("\x7f"))
	if p.query != "ga" || p.key([]byte("\r")) != pickChoose || p.chosen().SessionID != "c" {
		t.Fatal("backspace and choose")
	}
	p.key([]byte("\x1b[1;5C")) // unknown escape sequences are ignored
	if p.query != "ga" {
		t.Fatal("escape sequence typed into the filter")
	}
	if p.key([]byte("\x1b")) != pickCancel || p.key([]byte("\x03")) != pickCancel {
		t.Fatal("Esc and Ctrl-C cancel")
	}
	p = newPicker(rows)
	lines = p.render(50, 20, "/home/u", now)
	if lines[0] != "Resume Session (Current Folder)" || lines[2] != "> " || !strings.HasPrefix(lines[len(lines)-1], "(1/2)") {
		t.Fatalf("render %q", lines)
	}
	for _, l := range lines {
		plainLine := strings.NewReplacer(inverse, "", plain, "").Replace(l)
		if utf8.RuneCountInString(plainLine) > 49 {
			t.Fatalf("line wider than the terminal: %q", l)
		}
	}
	many := []sessionRow{}
	for i := 0; i < 40; i++ {
		many = append(many, sessionRow{SessionID: string(rune('A' + i)), Name: "s", ChangedAt: now})
	}
	p = newPicker(many)
	for i := 0; i < 30; i++ {
		p.key([]byte("\x0e"))
	}
	lines = p.render(80, 10, "", now)
	if len(lines) != 9 || !strings.Contains(lines[len(lines)-2], "> s") || !strings.HasPrefix(lines[len(lines)-1], "(31/40)") {
		t.Fatalf("scrolled render %q", lines)
	}
}
