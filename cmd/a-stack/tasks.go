package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mzmzeee/a-stack/internal/stack"
)

var taskCommands = []string{"task create", "task list", "task show", "task edit", "task assign", "task unassign", "task status", "task reopen", "task archive", "task unarchive", "task report", "task history", "task assignees", "task references", "task dependencies", "task handoff", "task attention set", "task attention resolve", "task reference add", "task reference remove", "dependency add", "dependency remove", "dependency history", "session snapshot", "session page", "answer"}

func isTaskCommand(command string) bool {
	for _, c := range taskCommands {
		if c == command {
			return true
		}
	}
	return false
}

type taskFlags struct {
	task, title, description, ticket, report, progress, reason, summary, question, questionID, text, note, location, referenceRevision, reference, from, to, body, bodyFile, pageKind string
	readRevision, knownRevision                                                                                                                                                       int64
	assign                                                                                                                                                                            []string
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
	fs.StringVar(&f.questionID, "question-id", "", "ID of the open Needs you question; task show prints it")
	fs.StringVar(&f.text, "text", "", "Your answer to the question")
	fs.StringVar(&f.location, "location", "", "File path or URL to show")
	fs.StringVar(&f.referenceRevision, "reference-revision", "", "Revision of the referenced file")
	fs.StringVar(&f.reference, "reference", "", "Reference ID")
	fs.StringVar(&f.from, "from", "", "Prerequisite Task ID")
	fs.StringVar(&f.to, "to", "", "Dependent Task ID")
	fs.StringVar(&f.body, "body", "", "Mail text")
	fs.StringVar(&f.bodyFile, "body-file", "", "Read the text from this file, or - for stdin")
	fs.StringVar(&f.pageKind, "page-kind", "", "tasks, agents, dependencies, assignments, references, history or graph_history")
	fs.Int64Var(&f.readRevision, "read-revision", 0, "Read at this Session read revision")
	fs.Int64Var(&f.knownRevision, "known-revision", 0, "Return unchanged when this is still the revision")
	fs.Func("assign", "Agent ID or name to assign; repeat for more", func(value string) error {
		f.assign = append(f.assign, value)
		return nil
	})
}
func taskAllowedFlags(command string) string {
	switch command {
	case "task create":
		return "operation-id title description body body-file ticket after assign"
	case "task list":
		return "limit after read-revision"
	case "task show":
		return "task read-revision"
	case "task handoff":
		return "task limit"
	case "task history", "task assignees", "task references", "task dependencies":
		return "task limit after read-revision"
	case "task edit":
		return "task operation-id expected-revision title description body body-file ticket"
	case "task assign", "task unassign":
		return "task operation-id expected-revision agent"
	case "task status":
		return "task operation-id expected-revision progress reason summary body body-file"
	case "task reopen":
		return "task operation-id expected-revision reason body body-file"
	case "task archive", "task unarchive":
		return "task operation-id expected-revision"
	case "task report":
		return "task operation-id expected-revision report body body-file"
	case "task attention set":
		return "task operation-id expected-revision question body body-file"
	case "task attention resolve":
		return "task operation-id expected-revision note body body-file"
	case "answer":
		return "task operation-id question-id text body body-file"
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

// textFlag names the flag of a command's one inline text. --body and
// --body-file can give the same text.
func textFlag(command, progress string) string {
	switch command {
	case "task create", "task edit":
		return "description"
	case "task report":
		return "report"
	case "task reopen":
		return "reason"
	case "task status":
		if progress == "finished" {
			return "summary"
		}
		return "reason"
	case "task attention set":
		return "question"
	case "task attention resolve":
		return "note"
	case "answer":
		return "text"
	}
	return ""
}

// readText puts the text of --body or --body-file into the command's text
// flag, and checks the text that the chosen progress needs. It runs before
// the CLI connects to the service.
func (f *taskFlags) readText(command string, seen map[string]bool) error {
	texts := map[string]*string{"description": &f.description, "report": &f.report, "reason": &f.reason, "summary": &f.summary, "question": &f.question, "note": &f.note, "text": &f.text}
	field := textFlag(command, f.progress)
	if seen["body"] || seen["body-file"] {
		if field == "" || seen[field] || seen["body"] && seen["body-file"] {
			return invalid("choose one of --" + field + ", --body and --body-file")
		}
		text := f.body
		if seen["body-file"] {
			var err error
			if text, err = bodyText(f.bodyFile); err != nil {
				return err
			}
		}
		seen[field] = true
		*texts[field] = text
	}
	switch f.progress {
	case "waiting", "cancelled", "finished":
		if command == "task status" && strings.TrimSpace(*texts[field]) == "" {
			return invalid("--" + field + " is required for --progress " + f.progress)
		}
	}
	return nil
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
		return nil, missingContext
	}
	ref := stack.Context{WorkspaceID: l.workspace, SessionID: l.session}
	path := "/v1/" + strings.ReplaceAll(command, " ", "/")
	var body any
	switch command {
	case "task create":
		body = stack.TaskCreateRequest{Context: ref, OperationID: l.operationID, Title: f.title, Description: f.description, TicketRef: f.ticket, PrerequisiteIDs: l.afters, AgentIDs: f.assign}
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
	case "answer":
		path = "/v1/task/answer"
		body = stack.AnswerRequest{Context: ref, OperationID: l.operationID, TaskID: f.task, QuestionID: f.questionID, Answer: f.text}
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

// unlinkedHint prints one line on stderr when task create made a Task without
// prerequisites while the Session has other open Tasks: a forgotten --after
// leaves the Operator's graph without lines. The data on stdout stays the
// receipt. A failed read prints nothing.
func unlinkedHint(ctx context.Context, client *stack.Client, ref stack.Context, created []byte, stderr io.Writer) {
	var receipt struct {
		Data stack.Receipt `json:"data"`
	}
	if json.Unmarshal(created, &receipt) != nil {
		return
	}
	tasks, err := listPages[stack.Task](ctx, client, "/v1/task/list", ref)
	if err != nil {
		return
	}
	open := 0
	for _, task := range tasks {
		if task.ID != receipt.Data.ResourceID && (task.Progress == "waiting" || task.Progress == "working") {
			open++
		}
	}
	if open > 0 {
		fmt.Fprintf(stderr, "a-stack: hint: the new Task has no prerequisites, and %d other Task(s) are open; give --after TASK_ID when it waits for one, or link it with dependency add\n", open)
	}
}
