package main

import (
	"context"
	"flag"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var taskCommands = []string{"task create", "task list", "task show", "task edit", "task assign", "task unassign", "task status", "task reopen", "task report", "task history", "task assignees", "task references", "task dependencies", "task attention set", "task attention resolve", "task reference add", "task reference remove", "dependency add", "dependency remove", "dependency history", "session snapshot", "session page"}

func isTaskCommand(command string) bool {
	for _, c := range taskCommands {
		if c == command {
			return true
		}
	}
	return false
}

type taskFlags struct {
	task, title, description, ticket, report, progress, reason, summary, question, note, location, referenceRevision, reference, from, to, bodyFile, pageKind string
	readRevision, knownRevision                                                                                                                               int64
}

func (f *taskFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.task, "task", "", "Task ID")
	fs.StringVar(&f.title, "title", "", "Task title")
	fs.StringVar(&f.description, "description", "", "Task description")
	fs.StringVar(&f.ticket, "ticket", "", "Ticket reference")
	fs.StringVar(&f.report, "report", "", "Task report")
	fs.StringVar(&f.progress, "progress", "", "waiting, working, finished or cancelled")
	fs.StringVar(&f.reason, "reason", "", "Why the Task waits, is cancelled or reopens")
	fs.StringVar(&f.summary, "summary", "", "Result summary of the finished Task")
	fs.StringVar(&f.question, "question", "", "Question for the Operator")
	fs.StringVar(&f.note, "note", "", "Note for the resolved question")
	fs.StringVar(&f.location, "location", "", "File path or URL to show")
	fs.StringVar(&f.referenceRevision, "reference-revision", "", "Revision of the referenced file")
	fs.StringVar(&f.reference, "reference", "", "Reference ID")
	fs.StringVar(&f.from, "from", "", "Prerequisite Task ID")
	fs.StringVar(&f.to, "to", "", "Dependent Task ID")
	fs.StringVar(&f.bodyFile, "body-file", "", "Read the text from this file, or - for stdin")
	fs.StringVar(&f.pageKind, "page-kind", "", "tasks, agents, dependencies, assignments, references, history or graph_history")
	fs.Int64Var(&f.readRevision, "read-revision", 0, "Read at this Session read revision")
	fs.Int64Var(&f.knownRevision, "known-revision", 0, "Return unchanged when this is still the revision")
}
func taskAllowedFlags(command string) string {
	switch command {
	case "task create":
		return "operation-id title description body-file ticket"
	case "task list":
		return "limit after read-revision"
	case "task show":
		return "task read-revision"
	case "task history", "task assignees", "task references", "task dependencies":
		return "task limit after read-revision"
	case "task edit":
		return "task operation-id expected-revision title description body-file ticket"
	case "task assign", "task unassign":
		return "task operation-id expected-revision agent"
	case "task status":
		return "task operation-id expected-revision progress reason summary body-file"
	case "task reopen":
		return "task operation-id expected-revision reason body-file"
	case "task report":
		return "task operation-id expected-revision report body-file"
	case "task attention set":
		return "task operation-id expected-revision question body-file"
	case "task attention resolve":
		return "task operation-id expected-revision note body-file"
	case "task reference add":
		return "task operation-id expected-revision location reference-revision"
	case "task reference remove":
		return "task operation-id expected-revision reference"
	case "dependency add", "dependency remove":
		return "operation-id expected-revision from to"
	case "dependency history":
		return "limit after read-revision"
	case "session snapshot":
		return "known-revision"
	case "session page":
		return "page-kind task limit after read-revision"
	}
	return ""
}
func bodyText(path string) (string, error) {
	var reader io.Reader = os.Stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", invalid("body file is unavailable")
		}
		defer file.Close()
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, stack.MaxTaskTextBytes+1))
	if err != nil || len(data) > stack.MaxTaskTextBytes || !utf8.Valid(data) {
		return "", invalid("body must be UTF-8 and at most 64 KiB")
	}
	return string(data), nil
}
func taskRequest(ctx context.Context, client *stack.Client, command string, l lifecycleFlags, f taskFlags, seen map[string]bool) ([]byte, error) {
	if l.workspace == "" || l.session == "" {
		return nil, invalid("explicit --workspace and --session required")
	}
	if seen["body-file"] {
		field := ""
		switch command {
		case "task create", "task edit":
			field = "description"
		case "task report":
			field = "report"
		case "task reopen":
			field = "reason"
		case "task status":
			field = "reason"
			if f.progress == "finished" {
				field = "summary"
			}
		case "task attention set":
			field = "question"
		case "task attention resolve":
			field = "note"
		}
		if field == "" || seen[field] {
			return nil, invalid("choose body text or --body-file")
		}
		text, err := bodyText(f.bodyFile)
		if err != nil {
			return nil, err
		}
		seen[field] = true
		switch field {
		case "description":
			f.description = text
		case "report":
			f.report = text
		case "reason":
			f.reason = text
		case "summary":
			f.summary = text
		case "question":
			f.question = text
		case "note":
			f.note = text
		}
	}
	ref := stack.Context{WorkspaceID: l.workspace, SessionID: l.session}
	path := "/v1/" + strings.ReplaceAll(command, " ", "/")
	var body any
	switch command {
	case "task create":
		body = stack.TaskCreateRequest{Context: ref, OperationID: l.operationID, Title: f.title, Description: f.description, TicketRef: f.ticket}
	case "task show":
		body = stack.TaskReadRequest{Context: ref, TaskID: f.task, ReadRevision: f.readRevision}
	case "task list", "task history", "task assignees", "task references", "task dependencies", "session page", "dependency history":
		kind := f.pageKind
		switch command {
		case "task list":
			kind = "tasks"
		case "task history":
			kind = "history"
		case "task assignees":
			kind = "assignments"
		case "task references":
			kind = "references"
		case "task dependencies":
			kind = "dependencies"
		case "dependency history":
			kind = "graph_history"
			path = "/v1/session/page"
		}
		if command != "task list" && strings.HasPrefix(command, "task ") && f.task == "" {
			return nil, invalid("explicit --task required")
		}
		body = stack.SnapshotPageRequest{Context: ref, Kind: kind, TaskID: f.task, ReadRevision: f.readRevision, After: l.after, Limit: l.limit}
	case "session snapshot":
		body = stack.SnapshotRequest{Context: ref, KnownRevision: f.knownRevision}
	case "dependency add", "dependency remove":
		body = stack.DependencyRequest{Context: ref, OperationID: l.operationID, ExpectedGraphRevision: l.revision, PrerequisiteID: f.from, DependentID: f.to}
	default:
		r := stack.TaskChangeRequest{Context: ref, OperationID: l.operationID, TaskID: f.task, ExpectedRevision: l.revision, AgentID: l.agent, Progress: f.progress, Reason: f.reason, Summary: f.summary, Question: f.question, Note: f.note, Location: f.location, ReferenceRevision: f.referenceRevision, ReferenceID: f.reference}
		if seen["title"] {
			r.Title = &f.title
		}
		if seen["description"] {
			r.Description = &f.description
		}
		if seen["ticket"] {
			r.TicketRef = &f.ticket
		}
		if seen["report"] {
			r.Report = &f.report
		}
		body = r
	}
	return client.Do(ctx, "POST", path, body)
}
