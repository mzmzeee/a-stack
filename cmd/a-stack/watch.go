package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// watcher runs `a-stack watch` and `a-stack status` for the Operator. Both
// only read: the Sessions of the current folder's Workspace, one Session
// (--session) or every Session (--all). Closed Sessions count only when
// --session names them.
func isWatchCommand(command string) bool { return command == "watch" || command == "status" }

// watchFlags are the flags of watch and status; all is the shared --all.
type watchFlags struct{ all, notify, notifyText bool }

func (f *watchFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.notify, "notify", false, "Also show a desktop notice through notify-send (osascript on macOS)")
	fs.BoolVar(&f.notifyText, "notify-text", false, "Put the question in the desktop notice (implies --notify)")
}

// watchCommand runs watch until Ctrl-C, or status once.
func watchCommand(ctx context.Context, connect func() (*stack.Client, error), command, session string, f watchFlags, machine bool, stdout, stderr io.Writer) error {
	if f.all && session != "" {
		return invalid("choose --all or --session")
	}

	client, err := connect()
	if err != nil {
		return err
	}

	w := &watcher{client: client, connect: connect, session: session, all: f.all, machine: machine, notify: f.notify || f.notifyText, notifyText: f.notifyText, stdout: stdout, stderr: stderr}
	w.setup()

	if command == "status" {
		return w.status(ctx)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	return w.watch(ctx)
}

type watcher struct {
	client             *stack.Client
	connect            func() (*stack.Client, error)
	session            string
	all, machine       bool
	notify, notifyText bool
	cwd, homeDir       string
	interval           time.Duration
	stdout, stderr     io.Writer
	notifier           func(summary, body string) error
	seen               map[string]*watchedSession
	failing            bool
	now                func() time.Time
	notifySend         string
}

type watchedSession struct {
	readRevision int64
	tasks        map[string]stack.Task
	// quietAt is when the next known Working Task turns quiet, which changes
	// no read revision; zero when none will.
	quietAt time.Time
}

// sessionStatus is one row of `a-stack status`. A Task that needs you is
// counted only there, not also as working or waiting. Quiet counts the
// Working Tasks that are also quiet. Finished counts the finished Tasks that
// are not archived.
type sessionStatus struct {
	Stalled       int    `json:"stalled"`
	WorkspaceID   string `json:"workspace_id"`
	WorkspacePath string `json:"workspace_path"`
	SessionID     string `json:"session_id"`
	Name          string `json:"name"`
	Closed        bool   `json:"closed"`
	NeedsYou      int    `json:"needs_you"`
	Working       int    `json:"working"`
	Quiet         int    `json:"quiet"`
	Waiting       int    `json:"waiting"`
	Finished      int    `json:"finished"`
}

// watchEvent is one change that watch prints.
type watchEvent struct {
	At        string `json:"at"`
	SessionID string `json:"session_id"`
	Session   string `json:"session"`
	TaskID    string `json:"task_id"`
	Title     string `json:"title"`
	Change    string `json:"change"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Text      string `json:"text,omitempty"`
}

type scopedSession struct {
	stack.Session
	path string
}

func (w *watcher) setup() {
	if wd, err := os.Getwd(); err == nil {
		if canonical, err := stack.ResolvePath(wd); err == nil {
			wd = canonical
		}

		w.cwd = wd
	}

	w.homeDir, _ = os.UserHomeDir()
	if w.now == nil {
		w.now = time.Now
	}

	if w.interval == 0 {
		w.interval = 2 * time.Second
	}

	w.seen = map[string]*watchedSession{}
}

// scope lists the Sessions to show, with their Workspace paths.
func (w *watcher) scope(ctx context.Context) ([]scopedSession, error) {
	workspaces, err := listPages[stack.Workspace](ctx, w.client, "/v1/workspace/list", stack.Context{})
	if err != nil {
		return nil, err
	}

	current := ""
	if w.session == "" && !w.all {
		for _, ws := range workspaces {
			if within(w.cwd, ws.Path) && len(ws.Path) > len(current) {
				current = ws.Path
			}
		}

		if current == "" {
			return nil, invalid("this folder has no Workspace; use --all or --session ID")
		}
	}

	found := []scopedSession{}
	for _, ws := range workspaces {
		if current != "" && ws.Path != current {
			continue
		}

		sessions, err := listPages[stack.Session](ctx, w.client, "/v1/session/list", stack.Context{WorkspaceID: ws.ID})
		if err != nil {
			return nil, err
		}

		for _, s := range sessions {
			if (w.session == "" && !s.Closed) || s.ID == w.session {
				found = append(found, scopedSession{s, ws.Path})
			}
		}
	}

	if w.session != "" && len(found) == 0 {
		return nil, &stack.Error{Code: "not_found", Message: "no Session with this ID"}
	}

	return found, nil
}

func (w *watcher) tasks(ctx context.Context, s scopedSession) ([]stack.Task, error) {
	return listPages[stack.Task](ctx, w.client, "/v1/task/list", stack.Context{WorkspaceID: s.WorkspaceID, SessionID: s.ID})
}

func countTasks(s scopedSession, tasks []stack.Task) sessionStatus {
	r := sessionStatus{WorkspaceID: s.WorkspaceID, WorkspacePath: s.path, SessionID: s.ID, Name: s.Name, Closed: s.Closed}
	for _, t := range tasks {
		for _, wait := range t.MailWaits {
			if wait.Stalled {
				r.Stalled++
				break
			}
		}

		switch {
		case t.NeedsYou:
			r.NeedsYou++
		case t.Progress == "working":
			r.Working++
			if t.Quiet {
				r.Quiet++
			}
		case t.Progress == "waiting":
			r.Waiting++
		case t.Progress == "finished" && !t.Archived:
			r.Finished++
		}
	}

	return r
}

// status prints one line per Session in scope.
func (w *watcher) status(ctx context.Context) error {
	sessions, err := w.scope(ctx)
	if err != nil {
		return err
	}

	rows := []sessionStatus{}
	for _, s := range sessions {
		tasks, err := w.tasks(ctx, s)
		if err != nil {
			return err
		}

		rows = append(rows, countTasks(s, tasks))
	}

	return w.printStatus(rows)
}

func (w *watcher) printStatus(rows []sessionStatus) error {
	if w.machine {
		return json.NewEncoder(w.stdout).Encode(map[string]any{"data": map[string]any{"sessions": rows}})
	}

	if len(rows) == 0 {
		_, err := io.WriteString(w.stdout, "No open Session here. To create one, run: a-stack new\n")
		return err
	}

	var b strings.Builder
	for _, r := range rows {
		closed := ""
		if r.Closed {
			closed = " (closed)"
		}

		fmt.Fprintf(&b, "%q in %s%s: %s\n", printable(r.Name), printable(shortenHome(r.WorkspacePath, w.homeDir)), closed, statusCounts(r))
	}

	_, err := io.WriteString(w.stdout, b.String())

	return err
}

func statusCounts(r sessionStatus) string {
	quiet := ""
	if r.Quiet > 0 {
		quiet = fmt.Sprintf(" (%d quiet)", r.Quiet)
	}

	text := fmt.Sprintf("%d needs you · %d working%s · %d waiting · %d finished", r.NeedsYou, r.Working, quiet, r.Waiting, r.Finished)
	if r.Stalled > 0 {
		text = fmt.Sprintf("%d Stalled · ", r.Stalled) + text
	}

	return text
}

// watch prints the status once, then one line per change until ctx ends.
// It reads a Session's Tasks again only when its read revision changed.
func (w *watcher) watch(ctx context.Context) error {
	if w.notify {
		tool := "notify-send"
		if runtime.GOOS == "darwin" {
			tool = "osascript"
		}

		if runtime.GOOS == "windows" {
			if w.notifier == nil {
				_, _ = io.WriteString(w.stderr, "Desktop notices are not available on Windows yet; watch shows no desktop notices.\n")
			}
		} else if path, err := exec.LookPath(tool); err == nil {
			w.notifySend = path
		} else if w.notifier == nil {
			_, _ = io.WriteString(w.stderr, tool+" is not installed; watch shows no desktop notices.\n")
		}
	}

	rows, err := w.poll(ctx, true)
	if err != nil {
		return err
	}

	if !w.machine {
		where := "every Session"
		if w.session == "" && !w.all {
			where = "the Sessions of " + printable(shortenHome(w.cwd, w.homeDir))
		} else if w.session != "" {
			where = "one Session"
		}

		fmt.Fprintf(w.stdout, "Watching %s. Press Ctrl-C to stop.\n", where)
	}

	if err = w.printStatus(rows); err != nil {
		return err
	}

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if w.failing && w.connect != nil {
			// A restarted service has a new instance and may have a new port.
			if client, err := w.connect(); err == nil {
				w.client = client
			}
		}

		_, err := w.poll(ctx, false)
		if ctx.Err() != nil {
			return nil
		}

		if err != nil && !w.failing {
			fmt.Fprintf(w.stderr, "a-stack watch: %s; trying again every %s.\n", errorText(err), w.interval)
		} else if err == nil && w.failing {
			_, _ = io.WriteString(w.stderr, "a-stack watch: connected again.\n")
		}

		w.failing = err != nil
	}
}

func errorText(err error) string {
	if e, ok := err.(*stack.Error); ok {
		return e.Message
	}

	return "the service did not answer"
}

// poll reads the Sessions in scope and the Tasks of each changed one, or of
// one with a Working Task that turns quiet by now. On the first poll it only
// records them and returns their status rows.
func (w *watcher) poll(ctx context.Context, first bool) ([]sessionStatus, error) {
	sessions, err := w.scope(ctx)
	if err != nil {
		return nil, err
	}

	rows := []sessionStatus{}
	for _, s := range sessions {
		known := w.seen[s.ID]
		if known != nil && known.readRevision == s.ReadRevision && (known.quietAt.IsZero() || w.now().Before(known.quietAt)) {
			continue
		}

		tasks, err := w.tasks(ctx, s)
		if err != nil {
			return nil, err
		}

		if first {
			rows = append(rows, countTasks(s, tasks))
		}

		if known == nil {
			known = &watchedSession{tasks: map[string]stack.Task{}}
			w.seen[s.ID] = known
		}

		if !first {
			if err = w.report(s, known.tasks, tasks); err != nil {
				return nil, err
			}
		}

		known.readRevision = s.ReadRevision

		known.tasks = map[string]stack.Task{}
		known.quietAt = time.Time{}
		for _, t := range tasks {
			known.tasks[t.ID] = t
			for _, wait := range t.MailWaits {
				at, err := time.Parse(time.RFC3339Nano, wait.Since)
				q := at.Add(stack.MailStalledAfter)
				if err == nil && !wait.Stalled && (known.quietAt.IsZero() || q.Before(known.quietAt)) {
					known.quietAt = q
				}
			}

			if q := t.QuietAt(); !t.Quiet && !q.IsZero() && (known.quietAt.IsZero() || q.Before(known.quietAt)) {
				known.quietAt = q
			}
		}
	}

	return rows, nil
}

// report prints the changes from before to now, rings the bell on a new
// Needs you or a Task that turns quiet, and sends the desktop notice of a new
// Needs you when it is on.
func (w *watcher) report(s scopedSession, before map[string]stack.Task, now []stack.Task) error {
	at := w.now().Format("15:04:05")
	events := []watchEvent{}
	add := func(t stack.Task, change, from, to, text string) {
		events = append(events, watchEvent{At: at, SessionID: s.ID, Session: s.Name, TaskID: t.ID, Title: t.Title, Change: change, From: from, To: to, Text: text})
	}

	questions := []string{}
	ring := false
	for _, t := range now {
		p, ok := before[t.ID]
		for _, wait := range t.MailWaits {
			if !wait.Stalled {
				continue
			}

			already := false
			for _, previous := range p.MailWaits {
				if previous.Stalled && previous.RecipientID == wait.RecipientID && previous.SenderName == wait.SenderName && previous.Since == wait.Since {
					already = true
					break
				}
			}

			if !already {
				add(t, "stalled", "", "", wait.Text)
				ring = true
			}
		}

		switch {
		case !ok:
			add(t, "created", "", t.Progress, "")
		case p.Progress != t.Progress:
			add(t, "progress", p.Progress, t.Progress, "")
		}

		if t.NeedsYou && !(ok && p.NeedsYou) {
			add(t, "needs_you", "", "", t.QuestionPreview)
			questions = append(questions, t.QuestionPreview)
		} else if ok && p.NeedsYou && !t.NeedsYou {
			add(t, "answered", "", "", "")
		}

		if ok && t.ReportAt != "" && t.ReportAt != p.ReportAt {
			add(t, "report", "", "", t.ReportPreview)
		}

		if t.Quiet && !(ok && p.Quiet) {
			add(t, "quiet", "", "", "")
			ring = true
		}
	}

	var b strings.Builder
	for _, e := range events {
		if w.machine {
			data, err := json.Marshal(map[string]any{"data": e})
			if err != nil {
				return err
			}

			b.Write(append(data, '\n'))
			continue
		}

		b.WriteString(eventLine(e))
	}

	if (len(questions) > 0 || ring) && !w.machine {
		b.WriteString("\a")
	}

	if _, err := io.WriteString(w.stdout, b.String()); err != nil {
		return err
	}

	if len(questions) > 0 && w.notify {
		count := countTasks(s, now).NeedsYou

		body := ""
		if w.notifyText {
			body = strings.Join(questions, " / ")
		}

		w.desktopNotice(fmt.Sprintf("a-stack: %d needs you in %s", count, printable(s.Name)), body)
	}

	return nil
}

func eventLine(e watchEvent) string {
	what := ""
	switch e.Change {
	case "created":
		what = "new Task, " + e.To
	case "progress":
		what = e.From + " → " + e.To
	case "needs_you":
		what = "needs you: " + e.Text
	case "answered":
		what = "Needs you answered"
	case "report":
		what = "new report: " + e.Text
	case "stalled":
		what = "Stalled: " + e.Text
	case "quiet":
		what = "quiet: Working, but no change or Task mail for " + quietText()
	}

	return fmt.Sprintf("%s  %s  %s: %s\n", e.At, printable(e.Session), printable(e.Title), printable(truncate(what, 120)))
}

// quietText says how long a Task goes without news before it is quiet.
func quietText() string {
	if stack.QuietAfter%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(stack.QuietAfter/time.Minute))
	}

	return stack.QuietAfter.String()
}

// desktopNotice runs notify-send, or osascript on macOS. A failure only
// costs the notice.
func (w *watcher) desktopNotice(summary, body string) {
	if w.notifier != nil {
		_ = w.notifier(summary, body)
		return
	}

	if w.notifySend == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, w.notifySend, noticeArgs(runtime.GOOS, summary, body)...).Run()
}

// noticeArgs are the arguments of notify-send, or of osascript on macOS.
func noticeArgs(goos, summary, body string) []string {
	if goos == "darwin" {
		// The texts go in as arguments, never into the script, and macOS
		// shows them as plain text.
		return []string{"-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", summary, printable(body)}
	}

	// -- keeps a text that starts with - from being read as an option.
	args := []string{"--app-name=a-stack", "--", summary}
	if body != "" {
		args = append(args, markupEscape(body))
	}

	return args
}

// markupEscape keeps notification servers that read a body as markup from
// reading Agent text as tags.
func markupEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(printable(s))
}

// statusLine prints one line for Claude Code's status line: what needs you
// and what works in the open Sessions of the Workspace of the session's
// folder, such as `a-stack Build: 1 needs you · 2 working`. It prints no view
// URL. On any error, or outside a Workspace, it prints nothing.
func statusLine(state string, stdin io.Reader, stdout io.Writer) {
	var in struct {
		Cwd       string `json:"cwd"`
		Workspace struct {
			CurrentDir string `json:"current_dir"`
		} `json:"workspace"`
	}
	if json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&in) != nil {
		return
	}

	dir := in.Workspace.CurrentDir
	if dir == "" {
		dir = in.Cwd
	}

	if canonical, err := stack.ResolvePath(dir); err == nil {
		dir = canonical
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, err := stack.NewClient(state, "", stack.OperatorCapability)
	if err != nil {
		return
	}

	w := &watcher{client: client, cwd: dir}

	sessions, err := w.scope(ctx)
	if err != nil || len(sessions) == 0 {
		return
	}

	total := sessionStatus{}
	for _, s := range sessions {
		tasks, err := w.tasks(ctx, s)
		if err != nil {
			return
		}

		r := countTasks(s, tasks)
		total.NeedsYou += r.NeedsYou
		total.Working += r.Working
	}

	name := "a-stack"
	if len(sessions) == 1 {
		name += " " + truncate(printable(sessions[0].Name), 40)
	}

	fmt.Fprintf(stdout, "%s: %d needs you · %d working\n", name, total.NeedsYou, total.Working)
}
