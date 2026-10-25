package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/mzmzeee/a-stack/internal/stack"
)

// handoff is the brief of `task handoff`: what a fresh Agent needs to take
// over a Task. It only reads, with the caller's credential, through the same
// endpoints as task show, task dependencies, task assignees and mail read.
type handoff struct {
	Task          stack.TaskDetail      `json:"task"`
	AskerName     string                `json:"asker_name,omitempty"`
	Prerequisites []handoffTask         `json:"unfinished_prerequisites"`
	Assignees     []stack.Assignment    `json:"assignees"`
	Mail          handoffMail           `json:"mail"`
	Orders        *stack.StandingOrders `json:"standing_orders,omitempty"`
	workspace     string
}
type handoffTask struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Progress string `json:"progress"`
	Reason   string `json:"reason,omitempty"`
}

// handoffMail holds the latest messages, oldest first. Readable is false when
// the credential may not read the Task's mail (only assignees may).
type handoffMail struct {
	Readable bool                `json:"readable"`
	Total    int64               `json:"total"`
	Messages []stack.MailMessage `json:"messages"`
}

// defaultHandoffMail is how many of the latest messages the brief shows
// without --limit.
const defaultHandoffMail = 20

func taskHandoff(ctx context.Context, client *stack.Client, l lifecycleFlags, f taskFlags, machine bool, stdout io.Writer) error {
	if l.workspace == "" || l.session == "" {
		return invalid("explicit --workspace and --session required")
	}
	if f.task == "" {
		return invalid("explicit --task required")
	}
	if l.limit < 0 || l.limit > 500 {
		return invalid("--limit takes 1 to 500 messages")
	}
	limit := l.limit
	if limit == 0 {
		limit = defaultHandoffMail
	}
	ref := stack.Context{WorkspaceID: l.workspace, SessionID: l.session}
	h := handoff{workspace: l.workspace, Prerequisites: []handoffTask{}, Assignees: []stack.Assignment{}, Mail: handoffMail{Messages: []stack.MailMessage{}}}
	if err := callData(ctx, client, "/v1/task/show", stack.TaskReadRequest{Context: ref, TaskID: f.task}, &h.Task); err != nil {
		return err
	}
	edges := []stack.Dependency{}
	err := sessionPages(ctx, client, stack.SnapshotPageRequest{Context: ref, Kind: "dependencies", TaskID: f.task}, func(d stack.Dependency) { edges = append(edges, d) })
	if err != nil {
		return err
	}
	for _, d := range edges {
		if d.PrerequisiteProgress == "finished" {
			continue
		}
		var p stack.TaskDetail
		if err = callData(ctx, client, "/v1/task/show", stack.TaskReadRequest{Context: ref, TaskID: d.PrerequisiteID}, &p); err != nil {
			return err
		}
		h.Prerequisites = append(h.Prerequisites, handoffTask{ID: p.ID, Title: p.Title, Progress: p.Progress, Reason: p.Reason})
	}
	if err = sessionPages(ctx, client, stack.SnapshotPageRequest{Context: ref, Kind: "assignments", TaskID: f.task}, func(a stack.Assignment) { h.Assignees = append(h.Assignees, a) }); err != nil {
		return err
	}
	if q := h.Task.Question; q != nil {
		h.AskerName = askerName(ctx, client, ref, h.Assignees, q.AskerID)
	}
	h.Mail.Total = h.Task.MailCount
	from := max(h.Task.MailCount-int64(limit), 0)
	var page stack.MailPage
	err = callData(ctx, client, "/v1/mail/read", stack.MailReadRequest{Context: ref, TaskID: f.task, Cursor: stack.MailCursor(f.task, from), Limit: limit}, &page)
	if e, ok := err.(*stack.Error); ok && e.Code == "forbidden" {
		err = nil
	} else if err == nil {
		h.Mail.Readable, h.Mail.Messages = true, page.Messages
	}
	if err != nil {
		return err
	}
	session, err := inspectSession(ctx, client, ref)
	if err != nil {
		return err
	}
	h.Orders = session.StandingOrders
	if machine {
		return json.NewEncoder(stdout).Encode(map[string]any{"data": h})
	}
	_, err = io.WriteString(stdout, h.text())
	return err
}

// callData sends one read and decodes its data.
func callData(ctx context.Context, client *stack.Client, path string, body, out any) error {
	data, err := client.Do(ctx, "POST", path, body)
	if err != nil {
		return err
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) != nil || json.Unmarshal(envelope.Data, out) != nil {
		return invalid("invalid service response")
	}
	return nil
}

// askerName finds the name of the Agent that asked, first among the
// assignees; it falls back to the ID.
func askerName(ctx context.Context, client *stack.Client, ref stack.Context, assignees []stack.Assignment, id string) string {
	for _, a := range assignees {
		if a.AgentID == id {
			return a.AgentName
		}
	}
	if name, err := agentName(ctx, client, ref, id); err == nil && name != "" {
		return name
	}
	return id
}

// text is the plain brief, in Markdown, with the IDs that the commands need.
func (h handoff) text() string {
	t := h.Task
	var b strings.Builder
	fmt.Fprintf(&b, "# Hand-over: %s\n\n", printable(t.Title))
	fmt.Fprintf(&b, "Task ID: %s\nSession ID: %s\nWorkspace ID: %s\n", t.ID, t.SessionID, h.workspace)
	if t.TicketRef != "" {
		fmt.Fprintf(&b, "Ticket: %s\n", printable(t.TicketRef))
	}
	fmt.Fprintf(&b, "Progress: %s, revision %d, last change %s\n", t.Progress, t.Revision, t.UpdatedAt)
	switch {
	case t.Progress == "finished" && t.Result != "":
		fmt.Fprintf(&b, "Result: %s\n", printable(t.Result))
	case t.Progress != "working" && t.Reason != "":
		fmt.Fprintf(&b, "Reason: %s\n", printable(t.Reason))
	}
	if t.Quiet {
		b.WriteString("Quiet: no change and no Task mail for " + quietText() + " or more\n")
	}
	if h.Orders != nil {
		section(&b, "Standing orders", "The Operator's rules for every Agent of the Workspace. They override your own defaults.\n\n"+h.Orders.Body, "")
	}
	section(&b, "Description", t.Description, "(none)")
	section(&b, "Report", t.Report, "(no report yet)")
	b.WriteString("\n## Needs you\n\n")
	if q := t.Question; q != nil {
		fmt.Fprintf(&b, "Open question from %s, asked %s, not answered yet:\n%s\n", printable(h.AskerName), q.AskedAt, block(q.Question))
	} else {
		b.WriteString("No open question.\n")
	}
	b.WriteString("\n## Unfinished prerequisites\n\n")
	if len(h.Prerequisites) == 0 {
		b.WriteString("None.\n")
	}
	for _, p := range h.Prerequisites {
		fmt.Fprintf(&b, "- %s (%s): %s", printable(p.Title), p.ID, p.Progress)
		if p.Reason != "" && p.Progress != "working" {
			b.WriteString(", " + printable(p.Reason))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Assignees\n\n")
	if len(h.Assignees) == 0 {
		b.WriteString("None: the Task is unassigned.\n")
	}
	for _, a := range h.Assignees {
		fmt.Fprintf(&b, "- @%s (%s)\n", printable(a.AgentName), a.AgentID)
	}
	switch {
	case !h.Mail.Readable:
		fmt.Fprintf(&b, "\n## Task mail\n\n%d messages. Only assignees read Task mail, and this credential is not one.\n", h.Mail.Total)
	case len(h.Mail.Messages) == 0:
		b.WriteString("\n## Task mail\n\nNo Task mail yet.\n")
	default:
		fmt.Fprintf(&b, "\n## Task mail, the last %d of %d\n", len(h.Mail.Messages), h.Mail.Total)
		for _, m := range h.Mail.Messages {
			fmt.Fprintf(&b, "\n[%d] %s %s:\n%s\n", m.Sequence, m.ReceivedAt, printable(m.SenderName), block(m.Body))
		}
	}
	fmt.Fprintf(&b, "\n## Next\n\nRead the Task again with `task show --task %s` for its current revision, and check your Task mail before you change anything. `mail read --task %s` has the whole mail history.\n", t.ID, t.ID)
	return b.String()
}

func section(b *strings.Builder, title, text, empty string) {
	if strings.TrimSpace(text) == "" {
		text = empty
	}
	b.WriteString("\n## " + title + "\n\n" + block(text) + "\n")
}

// block keeps the line breaks and tabs of a text from an Agent, replaces
// other control characters, and ends it without a trailing newline.
func block(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || unicode.IsPrint(r) {
			return r
		}
		return '\uFFFD'
	}, strings.ReplaceAll(s, "\r\n", "\n"))
	return strings.TrimRight(s, "\n")
}
