package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mzmzeee/a-stack/internal/stack"
)

type exportFlags struct {
	markdown, includeArchived bool
	output                    string
}

func (f *exportFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.markdown, "markdown", false, "Write Markdown (the default)")
	fs.BoolVar(&f.includeArchived, "include-archived", false, "Include archived Tasks")
	fs.StringVar(&f.output, "output", "", "Write to this file instead of stdout")
}

// sessionExport deliberately contains no Task mail, only its count. Both
// credential kinds use the same public reads, never mail read or mail check.
type sessionExport struct {
	SessionID     string                `json:"session_id"`
	Name          string                `json:"name"`
	WorkspaceID   string                `json:"workspace_id"`
	WorkspacePath string                `json:"workspace_path"`
	ReadRevision  int64                 `json:"read_revision"`
	Tasks         []exportTask          `json:"tasks"`
	Orders        *stack.StandingOrders `json:"standing_orders,omitempty"`
}

type exportTask struct {
	ID               string          `json:"id"`
	Title            string          `json:"title"`
	Progress         string          `json:"progress"`
	Archived         bool            `json:"archived"`
	Assignees        []string        `json:"assignees"`
	Result           string          `json:"result"`
	Report           string          `json:"report"`
	MailCount        int64           `json:"mail_count"`
	Question         *stack.Question `json:"question,omitempty"`
	AnsweredQuestion *stack.Question `json:"answered_question,omitempty"`
}

func exportSession(ctx context.Context, client *stack.Client, l lifecycleFlags, f exportFlags, machine bool, stdout io.Writer) error {
	if l.workspace == "" || l.session == "" {
		return missingContext
	}

	e, err := readSessionExport(ctx, client, stack.Context{WorkspaceID: l.workspace, SessionID: l.session}, f.includeArchived)
	if err != nil {
		return err // no partial stdout or output file on a failed collection
	}

	var data []byte
	if machine {
		data, err = json.Marshal(map[string]any{"data": e})
		data = append(data, '\n')
	} else {
		data = []byte(e.markdown())
	}

	if err != nil {
		return err
	}

	if f.output == "" {
		_, err = stdout.Write(data)
		return err
	}

	// Replace only after the complete export is written. A failed write must
	// not truncate an earlier export; a symlink must not overwrite its target.
	tmp, err := os.CreateTemp(filepath.Dir(f.output), ".a-stack-export-*")
	if err != nil {
		return invalid("cannot create --output file")
	}
	defer os.Remove(tmp.Name())

	_, err = tmp.Write(data)
	closed := tmp.Close()
	if err != nil || closed != nil {
		return invalid("cannot write --output file")
	}

	if err = stack.Rename(tmp.Name(), f.output); err != nil {
		return invalid("cannot replace --output file")
	}

	return nil
}

func readSessionExport(ctx context.Context, client *stack.Client, ref stack.Context, includeArchived bool) (sessionExport, error) {
	e := sessionExport{SessionID: ref.SessionID, WorkspaceID: ref.WorkspaceID, Tasks: []exportTask{}}
	var snapshot stack.SessionSnapshot
	if err := callData(ctx, client, "/v1/session/snapshot", stack.SnapshotRequest{Context: ref}, &snapshot); err != nil {
		return e, err
	}

	e.Name, e.ReadRevision, e.Orders = snapshot.Session.Name, snapshot.Session.ReadRevision, snapshot.Session.StandingOrders
	workspaces, err := listPages[stack.Workspace](ctx, client, "/v1/workspace/list", ref)
	if err != nil {
		return e, err
	}

	for _, w := range workspaces {
		if w.ID == ref.WorkspaceID {
			e.WorkspacePath = w.Path
		}
	}

	if e.WorkspacePath == "" {
		return e, invalid("Workspace folder missing from service response")
	}

	request := stack.SnapshotPageRequest{Context: ref, Kind: "tasks", ReadRevision: e.ReadRevision}
	var tasks []stack.Task
	if err = sessionPages(ctx, client, request, func(t stack.Task) { tasks = append(tasks, t) }); err != nil {
		return e, err
	}

	request.Kind = "dependencies"
	var edges []stack.Dependency
	if err = sessionPages(ctx, client, request, func(d stack.Dependency) { edges = append(edges, d) }); err != nil {
		return e, err
	}

	tasks, err = dependencyOrder(tasks, edges)
	if err != nil {
		return e, err
	}

	request.Kind = "assignments"
	assignees := map[string][]string{}
	if err = sessionPages(ctx, client, request, func(a stack.Assignment) { assignees[a.TaskID] = append(assignees[a.TaskID], a.AgentName) }); err != nil {
		return e, err
	}

	for _, t := range tasks {
		if t.Archived && !includeArchived {
			continue
		}

		var detail stack.TaskDetail
		if err = callData(ctx, client, "/v1/task/show", stack.TaskReadRequest{Context: ref, TaskID: t.ID, ReadRevision: e.ReadRevision}, &detail); err != nil {
			return e, err
		}

		names := append([]string{}, assignees[t.ID]...)
		slices.Sort(names)
		e.Tasks = append(e.Tasks, exportTask{ID: t.ID, Title: t.Title, Progress: t.Progress, Archived: t.Archived, Assignees: names, Result: detail.Result, Report: detail.Report, MailCount: t.MailCount, Question: detail.Question, AnsweredQuestion: detail.AnsweredQuestion})
	}

	return e, nil
}

// Visit prerequisites first, including archived ones before filtering. Titles
// and IDs break ties so repeated exports of an unchanged Session are stable.
func dependencyOrder(tasks []stack.Task, edges []stack.Dependency) ([]stack.Task, error) {
	byID := map[string]stack.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}

	compare := func(a, b stack.Task) int {
		if c := strings.Compare(a.Title, b.Title); c != 0 {
			return c
		}

		return strings.Compare(a.ID, b.ID)
	}

	prerequisites := map[string][]stack.Task{}
	for _, d := range edges {
		p, ok := byID[d.PrerequisiteID]
		if _, exists := byID[d.DependentID]; !ok || !exists {
			return nil, invalid("incomplete Task graph in service response")
		}

		prerequisites[d.DependentID] = append(prerequisites[d.DependentID], p)
	}

	slices.SortFunc(tasks, compare)

	for _, parents := range prerequisites {
		slices.SortFunc(parents, compare)
	}

	ordered := make([]stack.Task, 0, len(tasks))
	state := map[string]int{}

	var visit func(stack.Task) error
	visit = func(t stack.Task) error {
		if state[t.ID] == 1 {
			return invalid("cycle in service Task graph")
		}

		if state[t.ID] == 2 {
			return nil
		}

		state[t.ID] = 1
		for _, p := range prerequisites[t.ID] {
			if err := visit(p); err != nil {
				return err
			}
		}

		state[t.ID] = 2
		ordered = append(ordered, t)

		return nil
	}
	for _, t := range tasks {
		if err := visit(t); err != nil {
			return nil, err
		}
	}

	return ordered, nil
}

func (e sessionExport) markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session: %s\n\nWorkspace folder: %s\n", printable(e.Name), printable(e.WorkspacePath))

	if e.Orders != nil {
		section(&b, "Standing orders", e.Orders.Body, "")
	}

	b.WriteString("\n## Tasks\n\n")

	if len(e.Tasks) == 0 {
		b.WriteString("No Tasks.\n\n")
	}

	for _, t := range e.Tasks {
		fmt.Fprintf(&b, "### %s\n\nProgress: %s", printable(t.Title), t.Progress)

		if t.Archived {
			b.WriteString(" (archived)")
		}

		names := "Unassigned"
		if len(t.Assignees) > 0 {
			names = "@" + strings.Join(t.Assignees, ", @")
		}

		fmt.Fprintf(&b, "\nAssignees: %s\nTask mail count: %d (bodies omitted)\n\n", names, t.MailCount)
		fmt.Fprintf(&b, "#### Result\n\n%s\n\n#### Report\n\n%s\n\n", exportText(t.Result, "(no result yet)"), exportText(t.Report, "(no report yet)"))
	}

	b.WriteString("## Needs you\n\n")

	questions := false
	for _, t := range e.Tasks {
		if t.Question == nil && t.AnsweredQuestion == nil {
			continue
		}

		questions = true
		fmt.Fprintf(&b, "### %s\n\n", printable(t.Title))

		if q := t.Question; q != nil {
			fmt.Fprintf(&b, "Open question:\n\n%s\n\nNot answered yet.\n\n", block(q.Question))
		}

		if q := t.AnsweredQuestion; q != nil {
			fmt.Fprintf(&b, "Latest answered question:\n\n%s\n\nOperator's answer:\n\n%s\n\n", block(q.Question), block(q.Answer))
		}
	}

	if !questions {
		b.WriteString("No open or answered questions.\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func exportText(text, empty string) string {
	if strings.TrimSpace(text) == "" {
		return empty
	}

	return block(text)
}
